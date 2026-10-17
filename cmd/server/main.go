package main

import (
	"bytes"
	"log"
	"net/http"
	"os"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"
	"wiseql/internal/web/views"
)

func main() {
	if os.Getenv("APP_ENV") == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	assets, err := views.LoadAssets()
	if err != nil {
		log.Fatalf("load frontend assets: %v", err)
	}

	router := newRouter(assets)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if err := router.Run(":" + port); err != nil {
		log.Fatalf("start WiseQL server: %v", err)
	}
}

func newRouter(assets views.Assets) *gin.Engine {
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		panic(err)
	}
	router.Use(gin.Logger(), gin.Recovery())

	if os.Getenv("APP_ENV") == "production" {
		router.Static("/assets", "./web/dist/assets")
	}

	router.GET("/", func(c *gin.Context) {
		render(c, views.Home(assets))
	})
	router.GET("/fragments/status", func(c *gin.Context) {
		render(c, views.ServerStatus())
	})
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "wiseql", "status": "ok"})
	})
	return router
}

func render(c *gin.Context, component templ.Component) {
	var body bytes.Buffer
	if err := component.Render(c.Request.Context(), &body); err != nil {
		c.String(http.StatusInternalServerError, "render page: %v", err)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", body.Bytes())
}
