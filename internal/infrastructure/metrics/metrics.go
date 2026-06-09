// Package metrics exposes Prometheus counters and histograms for the entire platform.
// All modules use these — they never register their own collectors.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ─── HTTP ────────────────────────────────────────────────────────────────────

var RequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "spur_request_duration_seconds",
	Help:    "HTTP request latency.",
	Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5},
}, []string{"method", "path", "status", "module"})

var RequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_requests_total",
	Help: "Total HTTP requests.",
}, []string{"method", "path", "status", "module"})

var ActiveRequests = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "spur_active_requests",
	Help: "In-flight HTTP requests.",
}, []string{"module"})

// ─── Database ────────────────────────────────────────────────────────────────

var DBQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "spur_db_query_duration_seconds",
	Help:    "Database query latency.",
	Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1},
}, []string{"query_name", "module"})

var DBErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_db_errors_total",
	Help: "Database errors.",
}, []string{"query_name", "module"})

var DBPoolAcquired = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "spur_db_pool_acquired",
	Help: "Acquired DB connections.",
})

var DBPoolIdle = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "spur_db_pool_idle",
	Help: "Idle DB connections.",
})

// ─── Module ──────────────────────────────────────────────────────────────────

var ModuleErrorsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_module_errors_total",
	Help: "Module errors by type.",
}, []string{"module", "error_type"})

// Identity

var AuthAttemptsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_auth_attempts_total",
	Help: "Authentication attempts.",
}, []string{"method", "result"})

var OTPSentTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_otp_sent_total",
	Help: "OTPs sent by channel.",
}, []string{"channel"})

// Jobs

var JobExecutionsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_job_executions_total",
	Help: "Background job executions.",
}, []string{"task_type", "status"})

var JobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "spur_job_duration_seconds",
	Buckets: []float64{.1, .5, 1, 5, 10, 30, 60, 300},
}, []string{"task_type"})

var JobsQueued = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "spur_jobs_queued",
	Help: "Queued background jobs.",
})

// Agent / AI

var AgentTokensTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "spur_agent_tokens_total",
	Help: "LLM tokens consumed.",
}, []string{"model", "type"})

// ─── HTTP Middleware ──────────────────────────────────────────────────────────

// Middleware records request metrics. Mount at platform level, not per-module.
func Middleware(module string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ActiveRequests.WithLabelValues(module).Inc()
			defer ActiveRequests.WithLabelValues(module).Dec()

			rec := &statusRecorder{ResponseWriter: w, status: 200}
			next.ServeHTTP(rec, r)

			route := chi.RouteContext(r.Context()).RoutePattern()
			if route == "" {
				route = r.URL.Path
			}
			status := strconv.Itoa(rec.status)
			dur := time.Since(start).Seconds()
			RequestDuration.WithLabelValues(r.Method, route, status, module).Observe(dur)
			RequestsTotal.WithLabelValues(r.Method, route, status, module).Inc()
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Handler returns the Prometheus scrape endpoint. Mount on a separate internal port.
func Handler() http.Handler { return promhttp.Handler() }

// ─── Helpers ─────────────────────────────────────────────────────────────────

// TrackDBQuery records a DB query duration. Use with defer.
//
//	defer metrics.TrackDBQuery("GetUserByEmail", "identity", time.Now())
func TrackDBQuery(queryName, module string, start time.Time) {
	DBQueryDuration.WithLabelValues(queryName, module).Observe(time.Since(start).Seconds())
}

// TrackDBError increments the DB error counter.
func TrackDBError(queryName, module string) {
	DBErrorsTotal.WithLabelValues(queryName, module).Inc()
}

// RecordAuthAttempt records a login attempt.
// method: password|otp|magic_link   result: success|failed|locked|inactive
func RecordAuthAttempt(method, result string) {
	AuthAttemptsTotal.WithLabelValues(method, result).Inc()
}

// RecordOTPSent records an OTP delivery.
func RecordOTPSent(channel string) {
	OTPSentTotal.WithLabelValues(channel).Inc()
}
