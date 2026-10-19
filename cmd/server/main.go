package main

import (
	"bytes"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/a-h/templ"
	"github.com/gin-gonic/gin"
	"sqlwise/internal/web/views"
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
	server := &http.Server{
		Addr:              ":" + port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil {
		log.Fatalf("start sqlwise server: %v", err)
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
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "sqlwise", "status": "ok"})
	})
	return router
}

func render(c *gin.Context, component templ.Component) {
	var body bytes.Buffer
	if err := component.Render(c.Request.Context(), &body); err != nil {
		log.Printf("render page: %v", err)
		c.String(http.StatusInternalServerError, "internal server error")
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", body.Bytes())
}
