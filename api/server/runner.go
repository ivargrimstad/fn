package server

import (
	"bytes"
	"net/http"
	"path"
	"time"

	"github.com/fnproject/fn/api"
	"github.com/fnproject/fn/api/agent"
	"github.com/fnproject/fn/api/common"
	"github.com/fnproject/fn/api/models"
	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// handleFunctionCall executes the function, for router handlers
func (s *Server) handleFunctionCall(c *gin.Context) {
	err := s.handleFunctionCall2(c)
	if err != nil {
		handleErrorResponse(c, err)
	}
}

// handleFunctionCall2 executes the function and returns an error
// Requires the following in the context:
// * "app_name"
// * "path"
func (s *Server) handleFunctionCall2(c *gin.Context) error {
	ctx := c.Request.Context()
	var p string
	r := ctx.Value(api.Path)
	if r == nil {
		p = "/"
	} else {
		p = r.(string)
	}

	var a string
	ai := ctx.Value(api.AppName)
	if ai == nil {
		err := models.ErrAppsMissingName
		return err
	}
	a = ai.(string)

	// gin sets this to 404 on NoRoute, so we'll just ensure it's 200 by default.
	c.Status(200) // this doesn't write the header yet
	c.Header("Content-Type", "application/json")

	return s.serve(c, a, path.Clean(p))
}

// TODO it would be nice if we could make this have nothing to do with the gin.Context but meh
// TODO make async store an *http.Request? would be sexy until we have different api format...
func (s *Server) serve(c *gin.Context, appName, path string) error {
	// GetCall can mod headers, assign an id, look up the route/app (cached),
	// strip params, etc.
	call, err := s.agent.GetCall(
		agent.WithWriter(c.Writer), // XXX (reed): order matters [for now]
		agent.FromRequest(appName, path, c.Request),
	)
	if err != nil {
		return err
	}

	model := call.Model()
	{ // scope this, to disallow ctx use outside of this scope. add id for handleErrorResponse logger
		ctx, _ := common.LoggerWithFields(c.Request.Context(), logrus.Fields{"id": model.ID})
		c.Request = c.Request.WithContext(ctx)
	}

	if model.Type == "async" {
		// TODO we should push this into GetCall somehow (CallOpt maybe) or maybe agent.Queue(Call) ?
		contentLength := c.Request.ContentLength
		if contentLength < 128 { // contentLength could be -1 or really small, sanitize
			contentLength = 128
		}
		buf := bytes.NewBuffer(make([]byte, int(contentLength))[:0]) // TODO sync.Pool me
		_, err := buf.ReadFrom(c.Request.Body)
		if err != nil {
			return models.ErrInvalidPayload
		}
		model.Payload = buf.String()

		// TODO idk where to put this, but agent is all runner really has...
		err = s.agent.Enqueue(c.Request.Context(), model)
		if err != nil {
			return err
		}

		c.JSON(http.StatusAccepted, map[string]string{"call_id": model.ID})
		return nil
	}

	err = s.agent.Submit(call)
	if err != nil {
		// NOTE if they cancel the request then it will stop the call (kind of cool),
		// we could filter that error out here too as right now it yells a little
		if err == models.ErrCallTimeoutServerBusy || err == models.ErrCallTimeout {
			// TODO maneuver
			// add this, since it means that start may not have been called [and it's relevant]
			c.Writer.Header().Add("XXX-FXLB-WAIT", time.Now().Sub(time.Time(model.CreatedAt)).String())
		}
		// NOTE: if the task wrote the headers already then this will fail to write
		// a 5xx (and log about it to us) -- that's fine (nice, even!)
		return err
	}

	// TODO plumb FXLB-WAIT somehow (api?)

	// TODO we need to watch the response writer and if no bytes written
	// then write a 200 at this point?
	// c.Data(http.StatusOK)

	return nil
}
