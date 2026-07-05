package http

import (
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/ranakdinesh/setika/internal/logger"
)

func RequestLogger(log *logger.Loggerx) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()

			// Ensure a trace_id exists
			traceID := r.Header.Get("X-Request-Id")
			if traceID == "" {
				traceID = middleware.GetReqID(r.Context())
			}
			ctx := logger.WithTraceID(r.Context(), traceID)

			next.ServeHTTP(ww, r.WithContext(ctx))

			lat := time.Since(start)
			remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)

			log.Info(ctx).
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Int("status", ww.Status()).
				Int("bytes", ww.BytesWritten()).
				Str("remote_ip", remoteIP).
				Dur("latency", lat).
				Msg("http_request")
		})
	}
}
