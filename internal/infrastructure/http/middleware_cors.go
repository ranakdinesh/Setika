package http

import (
	"net/http"
	"strings"
)

func CORS(opts Options) func(http.Handler) http.Handler {
	allowedOrigins := make(map[string]bool)
	for _, o := range opts.AllowedOrigins {
		allowedOrigins[o] = true
	}
	// If a dynamic origin validator is configured, do not let "*" become
	// blanket credentialed CORS. Tenant origins must pass validation.
	allowAll := opts.OriginAllowed == nil && (len(opts.AllowedOrigins) == 0 || allowedOrigins["*"])

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Dynamic Origin Handling
			if allowAll {
				if origin != "" {
					w.Header().Set("Access-Control-Allow-Origin", origin)
				} else {
					w.Header().Set("Access-Control-Allow-Origin", "*")
				}
			} else if allowedOrigins[origin] || (opts.OriginAllowed != nil && opts.OriginAllowed(r.Context(), origin)) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else {
				// If origin not allowed, we don't set the header, browser will block it.
			}

			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Methods", strings.Join(opts.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(opts.AllowedHeaders, ", "))
			w.Header().Add("Vary", "Origin")

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
