package http

import (
	"context"
	"crypto/rsa"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

// --- 1. Context Keys (Type Safe) ---
type contextKey string

const (
	TenantIDKey     contextKey = "tenant_id"
	UserIDKey       contextKey = "user_id"
	RolesKey        contextKey = "roles"       // []string (Role Names)
	PermissionsKey  contextKey = "permissions" // map[string]bool (Active Permissions)
	IsSuperAdminKey contextKey = "is_super_admin"
	AuthzVersionKey contextKey = "authz_version"
)

// --- 2. The AuthGuard ---
type AuthGuard struct {
	publicKey *rsa.PublicKey
}

func NewAuthGuard(key *rsa.PublicKey) (*AuthGuard, error) {
	if key == nil {
		return nil, errors.New("AuthGuard: public key cannot be nil")
	}
	return &AuthGuard{publicKey: key}, nil
}

// ChiMiddleware validates the JWT and injects UserID, TenantID, and Roles.
func (a *AuthGuard) ChiMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenString := ""

		// 1. Try Authorization Header first (API Clients)
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) == 2 && parts[0] == "Bearer" {
				tokenString = parts[1]
			}
		}

		// 2. If Header missing, Try Cookie (Browser Dashboard)
		if tokenString == "" {
			// Ensure this name matches what you used in session.Manager.WriteSessionCookie
			// Based on standard practices, let's assume "access_token" or "session_token"
			cookie, err := r.Cookie("spur_access_token")
			if err == nil {
				tokenString = cookie.Value
			} else {
				// DEBUG LOG: See if the browser actually sent it
			}
		}

		// 3. If still empty, Block Access
		if tokenString == "" {
			// If it's a browser request (HTML), redirect to login instead of JSON error
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/auth/login?return_to="+r.URL.String(), http.StatusFound)
				return
			}
			http.Error(w, "missing authorization token", http.StatusUnauthorized)
			return
		}

		// 4. Parse & Validate
		token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
			if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
				return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
			}
			return a.publicKey, nil
		})

		if err != nil || !token.Valid {
			if strings.Contains(r.Header.Get("Accept"), "text/html") {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}

		// C. Extract Claims
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			http.Error(w, "invalid token claims", http.StatusUnauthorized)
			return
		}

		// D. Build Context
		ctx := r.Context()

		// 1. Tenant ID
		if tenantID, ok := claims["tid"].(string); ok {
			ctx = context.WithValue(ctx, TenantIDKey, tenantID)
		} else if tenantID, ok := claims["tenant_id"].(string); ok {
			// Fallback for different naming conventions
			ctx = context.WithValue(ctx, TenantIDKey, tenantID)
		}

		// 2. User ID (Subject)
		if sub, ok := claims["sub"].(string); ok {
			ctx = context.WithValue(ctx, UserIDKey, sub)
		}

		// 3. Roles (Critical for Performance)
		// We extract roles here, but we do NOT resolve permissions yet.
		var roles []string
		if rolesRaw, ok := claims["roles"].([]interface{}); ok {
			for _, r := range rolesRaw {
				if rStr, ok := r.(string); ok {
					roles = append(roles, rStr)
				}
			}
		}
		ctx = context.WithValue(ctx, RolesKey, roles)

		// E. Next
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// --- 3. Helper Accessors ---

func GetUserID(ctx context.Context) string {
	if v, ok := ctx.Value(UserIDKey).(string); ok {
		return v
	}
	return ""
}

func GetTenantID(ctx context.Context) string {
	if v, ok := ctx.Value(TenantIDKey).(string); ok {
		return v
	}
	return ""
}

func SetTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, TenantIDKey, tenantID)
}

func GetRoles(ctx context.Context) []string {

	if v, ok := ctx.Value(RolesKey).([]string); ok {
		return v
	}
	return nil
}

// IsSuperAdmin returns true when the request context contains a super-admin claim.
// The claim is set by ChiMiddleware when the JWT contains "sa": true.
func IsSuperAdmin(ctx context.Context) bool {
	if v, ok := ctx.Value(contextKey("is_super_admin")).(bool); ok {
		return v
	}
	// Also check roles for backward-compat
	for _, r := range GetRoles(ctx) {
		if r == "saas-admin" || r == "SUPER_ADMIN" {
			return true
		}
	}
	return false
}
