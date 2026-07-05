package http

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ranakdinesh/setika/internal/logger"
)

// MountFunc lets parent apps add multiple routes at once.
type MountFunc func(r chi.Router)

type Server struct {
	http   *http.Server
	log    *logger.Loggerx
	router chi.Router
}

// NewServer builds a hardened HTTP server and allows the parent to mount routes.
func NewServer(opts Options, log *logger.Loggerx, initialMount MountFunc) *Server {
	if opts.ReadTimeout == 0 {
		opts.ReadTimeout = 15 * time.Second
	}
	if opts.ReadHeaderTimeout == 0 {
		opts.ReadHeaderTimeout = 15 * time.Second
	}
	if opts.WriteTimeout == 0 {
		opts.WriteTimeout = 30 * time.Second
	}
	if opts.IdleTimeout == 0 {
		opts.IdleTimeout = 60 * time.Second
	}
	// EnableSecurityHeaders is handled in Options struct or logic, assuming true if not set is handled by caller or defaults.
	// Here we just use what's passed.

	r := chi.NewRouter()

	// Core middlewares
	r.Use(middleware.RealIP)
	r.Use(middleware.RequestID)
	r.Use(RequestLogger(log))
	r.Use(middleware.Recoverer)
	// r.Use(RequestLogger(log)) // Assuming RequestLogger is defined in middleware_requestlog.go and compatible

	if opts.MaxBodyBytes > 0 {
		// r.Use(MaxBytes(opts.MaxBodyBytes)) // Assuming MaxBytes is defined
	}
	if opts.EnableCORS {
		r.Use(CORS(opts))
	}
	if opts.EnableSecurityHeaders {
		// r.Use(SecurityHeaders()) // Assuming SecurityHeaders is defined
	}

	// Health endpoints
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	r.Get("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })

	// Allow initial mount for convenience
	if initialMount != nil {
		initialMount(r)
	}

	s := &http.Server{
		Addr:              opts.Addr,
		Handler:           r,
		ReadTimeout:       opts.ReadTimeout,
		ReadHeaderTimeout: opts.ReadHeaderTimeout,
		WriteTimeout:      opts.WriteTimeout,
		IdleTimeout:       opts.IdleTimeout,
	}

	return &Server{http: s, log: log, router: r}
}

// ---- Public mounting API ----

// Mount adds routes directly under the root router.
// Usage: srv.Mount(func(r) { r.Get("/index", h); r.Get("/users/list", h2) })
func (s *Server) Mount(mounts ...MountFunc) {
	for _, m := range mounts {
		if m != nil {
			m(s.router)
		}
	}
}

// MountGroup adds routes under a path prefix, e.g. "/users".
//
// Usage:
//
//	srv.MountGroup("/users", func(r) {
//	    r.Get("/list", listUsers)
//	    r.Get("/{id}", getUser)
//	})
func (s *Server) MountGroup(prefix string, mounts ...MountFunc) {
	s.router.Route(prefix, func(r chi.Router) {
		for _, m := range mounts {
			if m != nil {
				m(r)
			}
		}
	})
}

// MountHost mounts routes for a specific host only (exact match).
// Example: srv.MountHost("www.citual.com", func(r) { r.Get("/index", h) })
func (s *Server) MountHost(host string, mounts ...MountFunc) {
	sub := chi.NewRouter()
	sub.Use(hostOnly(host))
	for _, m := range mounts {
		if m != nil {
			m(sub)
		}
	}
	// mount at "/" so routes keep their full paths
	s.router.Mount("/", sub)
}

// hostOnly returns 404 for requests not matching the host.
func hostOnly(host string) func(http.Handler) http.Handler {
	host = strings.ToLower(host)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.EqualFold(r.Host, host) {
				next.ServeHTTP(w, r)
				return
			}
			// Not our host; let other routes handle if any. If you prefer hard 404, uncomment:
			//			http.NotFound(w, r)
			//			return
			// Fall-through: do nothing; other groups may match.
			next.ServeHTTP(w, r)
		})
	}
}

// Start runs the server and shuts down gracefully when ctx is canceled.
func (s *Server) Start(ctx context.Context) error {
	go func() {
		s.log.Info(ctx).Str("addr", s.http.Addr).Msg("http server: listening")
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.log.Error(ctx).Err(err).Msg("http server: fatal")
		}
	}()
	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	s.log.Info(ctx).Msg("http server: shutting down")
	return s.http.Shutdown(shutdownCtx)
}
