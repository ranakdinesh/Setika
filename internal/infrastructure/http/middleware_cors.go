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
	// If empty, assume * (dev mode)
	allowAll := len(opts.AllowedOrigins) == 0

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")

			// Dynamic Origin Handling
			if allowAll {
				w.Header().Set("Access-Control-Allow-Origin", "*")
			} else if allowedOrigins[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
			} else {
				// If origin not allowed, we don't set the header, browser will block it.
			}

			w.Header().Set("Access-Control-Allow-Methods", strings.Join(opts.AllowedMethods, ", "))
			w.Header().Set("Access-Control-Allow-Headers", strings.Join(opts.AllowedHeaders, ", "))

			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
