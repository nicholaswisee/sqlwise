package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/handlers"
	"sqlwise/internal/web/views"
)

func main() {
	if os.Getenv("APP_ENV") == "production" {
		gin.SetMode(gin.ReleaseMode)
	}
	service, pool, err := connectAuth()
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()
	if err := checkMigrationSchema(pool); err != nil {
		log.Fatal(err)
	}
	if err := checkConnectionSchema(pool); err != nil {
		log.Fatal(err)
	}
	targets, closeTargets, err := loadDatabaseTargets(pool)
	if err != nil {
		log.Fatal(err)
	}
	defer closeTargets()
	policy := access.NewService(access.NewStore(pool), service)
	router := newAPIRouter(service, policy)
	registerDatabaseAPIRoutes(router, service, policy, pool, targets)
	if err := registerSPA(router, "web/spa/dist/client"); err != nil {
		if os.Getenv("APP_ENV") == "production" {
			log.Fatal(err)
		}
		log.Print("SPA assets unavailable; use the SPA development server or run pnpm run spa:build")
	}
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

func newHTTPRouter() *gin.Engine {
	router := gin.New()
	if err := router.SetTrustedProxies(nil); err != nil {
		panic(err)
	}
	router.Use(gin.LoggerWithFormatter(func(p gin.LogFormatterParams) string {
		return fmt.Sprintf("http method=%s status=%d\n", p.Method, p.StatusCode)
	}), func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				log.Print("http request failed")
				if c.Request.URL.Path == "/api" || strings.HasPrefix(c.Request.URL.Path, "/api/") {
					c.Header("Cache-Control", "no-store")
					c.AbortWithStatusJSON(http.StatusInternalServerError, gin.H{"error": gin.H{"code": "internal_error", "message": "The request could not be completed.", "fields": gin.H{}}})
					return
				}
				c.AbortWithStatus(http.StatusInternalServerError)
			}
		}()
		c.Next()
	})
	router.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"service": "sqlwise", "status": "ok"})
	})
	return router
}

// newRouter retains the Milestone 1 HTML router for legacy regression tests.
// The application entry point mounts newAPIRouter and the static SPA instead.
func newRouter(assets views.Assets, service *auth.Service, policies ...*access.Service) *gin.Engine {
	router := newHTTPRouter()

	if os.Getenv("APP_ENV") == "production" {
		router.Static("/assets", "./web/dist/assets")
	}

	router.GET("/", func(c *gin.Context) {
		handlers.Render(c, http.StatusOK, views.Home(assets))
	})
	if service != nil {
		var policy *access.Service
		if len(policies) > 0 {
			policy = policies[0]
		}
		authHandler := handlers.NewAuth(service, assets, os.Getenv("APP_ENV") == "production", policy)
		authHandler.Register(router)
		if policy != nil {
			handlers.NewAccessAdmin(authHandler, policy, assets).Register(router)
		}
	}
	return router
}
