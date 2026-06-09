package http

import (
	"time"

	"go.opentelemetry.io/otel/trace"
)

type Options struct {
	// Listen address, e.g. ":8080"
	Addr string

	// Per-request read/handler/write/idle timeouts
	ReadTimeout       time.Duration
	ReadHeaderTimeout time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration

	// Max request body size in bytes (0 = unlimited; recommended: 10<<20 for 10MB)
	MaxBodyBytes int64

	// CORS
	EnableCORS     bool
	AllowedOrigins []string // nil => ["*"]
	AllowedMethods []string // nil => GET,POST,PUT,PATCH,DELETE,OPTIONS
	AllowedHeaders []string // nil => common headers

	// Security headers (on by default unless explicitly disabled)
	EnableSecurityHeaders bool
	TracerProvider        trace.TracerProvider
}
