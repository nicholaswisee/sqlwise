package main

import (
	"os"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"
	"sqlwise/internal/access"
	"sqlwise/internal/auth"
	"sqlwise/internal/database"
	"sqlwise/internal/handlers"
	"sqlwise/internal/migration"
	"sqlwise/internal/queryplan"
	"sqlwise/internal/schemadiff"
	"sqlwise/internal/web/views"
)

func newAPIRouter(service *auth.Service, policy *access.Service) *gin.Engine {
	router := newHTTPRouter()
	router.HandleMethodNotAllowed = true
	router.NoRoute(func(c *gin.Context) { frontendMiss(c, 404) })
	router.NoMethod(func(c *gin.Context) { frontendMiss(c, 405) })
	if service != nil {
		guard := handlers.NewAuth(service, views.Assets{}, os.Getenv("APP_ENV") == "production", policy)
		guard.RegisterAPI(router)
		if policy != nil {
			handlers.NewAccessAdmin(guard, policy, views.Assets{}).RegisterAPI(router)
		}
	}
	return router
}

func registerDatabaseAPIRoutes(router *gin.Engine, service *auth.Service, policy *access.Service, pool *pgxpool.Pool, targets databaseTargets) {
	guard := handlers.NewAuth(service, views.Assets{}, os.Getenv("APP_ENV") == "production", policy)
	migrations := migration.NewService(migration.NewStore(pool), policy, targets.targets, time.Now)
	diff := schemadiff.NewService(policy, targets.snapshot, migrations)
	var resolver database.Resolver
	if targets.pools != nil {
		resolver = targets.pools
	}
	header := handlers.NewWorkspaceHeader(migrations, resolver)
	handlers.NewMigrations(guard, policy, migrations, diff, views.Assets{}).WithHeader(header).RegisterAPI(router)
	if targets.pools != nil {
		handlers.NewDatabaseViewer(guard, policy, targets.pools, views.Assets{}).WithHeader(header).RegisterAPI(router)
		handlers.NewExplain(guard, policy, queryplan.NewService(policy, targets.pools), views.Assets{}).RegisterAPI(router)
	}
	if targets.store != nil {
		handlers.NewConnectionAdmin(guard, policy, targets.store, targets.pools, views.Assets{}).RegisterAPI(router)
	}
}
