package handlers

import (
	"bytes"
	"net/http"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"
)

func Render(c *gin.Context, status int, component templ.Component) {
	var body bytes.Buffer
	if err := component.Render(c.Request.Context(), &body); err != nil {
		c.String(http.StatusInternalServerError, "Unable to display this page.")
		return
	}
	c.Data(status, "text/html; charset=utf-8", body.Bytes())
}
