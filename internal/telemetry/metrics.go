package telemetry

import (
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var platformPool atomic.Pointer[pgxpool.Pool]

var registry = prometheus.NewRegistry()

var requests = prometheus.NewCounterVec(prometheus.CounterOpts{
	Name: "sqlwise_http_requests_total",
	Help: "HTTP requests by registered route, method, and status.",
}, []string{"route", "method", "status"})

var requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "sqlwise_http_request_duration_seconds",
	Help:    "HTTP request duration by registered route and method.",
	Buckets: prometheus.DefBuckets,
}, []string{"route", "method"})

func init() {
	registry.MustRegister(requests, requestDuration)
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "sqlwise_db_pool_active_connections",
		Help: "Acquired platform database connections.",
	}, func() float64 {
		if pool := platformPool.Load(); pool != nil {
			return float64(pool.Stat().AcquiredConns())
		}
		return 0
	}))
	registry.MustRegister(prometheus.NewGaugeFunc(prometheus.GaugeOpts{
		Name: "sqlwise_db_pool_idle_connections",
		Help: "Idle platform database connections.",
	}, func() float64 {
		if pool := platformPool.Load(); pool != nil {
			return float64(pool.Stat().IdleConns())
		}
		return 0
	}))
	registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Name: "sqlwise_db_pool_acquire_seconds_total",
		Help: "Cumulative time spent acquiring platform database connections.",
	}, func() float64 {
		if pool := platformPool.Load(); pool != nil {
			return pool.Stat().AcquireDuration().Seconds()
		}
		return 0
	}))
}

func BindPlatformPool(pool *pgxpool.Pool) {
	platformPool.Store(pool)
}

func MetricsHandler() http.Handler {
	return promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
}

func normalizedHTTPMethod(method string) string {
	switch method {
	case http.MethodGet:
		return http.MethodGet
	case http.MethodHead:
		return http.MethodHead
	case http.MethodPost:
		return http.MethodPost
	case http.MethodPut:
		return http.MethodPut
	case http.MethodPatch:
		return http.MethodPatch
	case http.MethodDelete:
		return http.MethodDelete
	case http.MethodOptions:
		return http.MethodOptions
	default:
		return "OTHER"
	}
}

func recordHTTP(method, route string, status int, duration time.Duration) {
	if route == "" {
		route = "unmatched"
	}
	requests.WithLabelValues(route, method, strconv.Itoa(status)).Inc()
	requestDuration.WithLabelValues(route, method).Observe(duration.Seconds())
}
