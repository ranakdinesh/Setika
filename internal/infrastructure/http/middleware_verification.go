package http

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"
)

// ─── VerificationProvider ────────────────────────────────────────────────────

// VerificationProvider is implemented by the identity module's AuthService.
// Other packages depend on this interface, not the concrete service.
type VerificationProvider interface {
	GetVerificationStatus(ctx context.Context, userID uuid.UUID, tenantID uuid.UUID) (*VerificationStatus, error)
}

// VerificationStatus carries the result of an email/mobile verification check.
type VerificationStatus struct {
	IsVerified         bool
	GracePeriodExpired bool
}

// ─── VerificationGuard ───────────────────────────────────────────────────────

// VerificationGuard middleware blocks requests from users whose verification
// grace period has expired without completing email/mobile verification.
type VerificationGuard struct {
	provider VerificationProvider
}

// NewVerificationGuard creates a guard backed by the given provider.
func NewVerificationGuard(provider VerificationProvider) *VerificationGuard {
	return &VerificationGuard{provider: provider}
}

// ChiMiddleware enforces verification on protected routes.
func (g *VerificationGuard) ChiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userIDStr := GetUserID(r.Context())
		tenantIDStr := GetTenantID(r.Context())

		if userIDStr == "" || tenantIDStr == "" {
			next.ServeHTTP(w, r)
			return
		}

		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}
		tenantID, err := uuid.Parse(tenantIDStr)
		if err != nil {
			next.ServeHTTP(w, r)
			return
		}

		status, err := g.provider.GetVerificationStatus(r.Context(), userID, tenantID)
		if err != nil {
			// On error, allow through — don't block users due to infra issues
			next.ServeHTTP(w, r)
			return
		}

		if status.GracePeriodExpired && !status.IsVerified {
			http.Error(w, `{"error":"account_not_verified","message":"Please verify your email or mobile to continue."}`, http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// ─── APIKeyGuard ─────────────────────────────────────────────────────────────

// APIKeyAuthenticator is implemented by the identity module's APIKeyService.
type APIKeyAuthenticator interface {
	Authenticate(ctx context.Context, fullKey string, origin string, clientIP string) (interface{}, error)
}

// APIKeyGuard validates API key authentication on routes that accept it.
type APIKeyGuard struct {
	svc APIKeyAuthenticator
}

// NewAPIKeyGuard creates a guard backed by the given authenticator.
func NewAPIKeyGuard(svc APIKeyAuthenticator) *APIKeyGuard {
	return &APIKeyGuard{svc: svc}
}

// ChiMiddleware validates an API key from the X-API-Key header.
func (g *APIKeyGuard) ChiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("X-API-Key")
		if key == "" {
			http.Error(w, `{"error":"missing_api_key"}`, http.StatusUnauthorized)
			return
		}

		origin := r.Header.Get("Origin")
		_, err := g.svc.Authenticate(r.Context(), key, origin, requestIP(r))
		if err != nil {
			http.Error(w, `{"error":"invalid_api_key"}`, http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func requestIP(r *http.Request) string {
	if forwarded := strings.TrimSpace(r.Header.Get("X-Forwarded-For")); forwarded != "" {
		parts := strings.Split(forwarded, ",")
		return strings.TrimSpace(parts[0])
	}
	if realIP := strings.TrimSpace(r.Header.Get("X-Real-IP")); realIP != "" {
		return realIP
	}
	return r.RemoteAddr
}
