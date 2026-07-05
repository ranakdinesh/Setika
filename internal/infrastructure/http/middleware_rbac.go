package http

import (
	"context"
	"net/http"
	"sync"
	// "github.com/ranakdinesh/setika/internal/logger"
)

// PermissionResolver defines how we fetch permissions from the DB (if not in cache)
type PermissionResolver interface {
	// GetPermissionsForRole returns all permission slugs (e.g., "identity.users.create") for a role
	GetPermissionsForRole(ctx context.Context, role string) ([]string, error)
}

// RBACMiddleware handles granular permissions efficiently
type RBACMiddleware struct {
	resolver PermissionResolver
	cache    sync.Map // Simple in-memory cache: [role_name] -> []permissions
}

func NewRBACMiddleware(resolver PermissionResolver) *RBACMiddleware {
	return &RBACMiddleware{
		resolver: resolver,
	}
}

// PopulatePermissions is a middleware that resolves Roles -> Permissions
// and injects them into the context as a map for O(1) lookups.
func (m *RBACMiddleware) PopulatePermissions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		roles := GetRoles(ctx)

		// Map for O(1) checks: "identity.users.view" -> true
		permMap := make(map[string]bool)

		// Optimization: If Super Admin, maybe grant all? (Optional logic)
		// if contains(roles, "saas-admin") { ... }

		for _, role := range roles {
			// 1. Check Cache
			perms, ok := m.getCachedPermissions(role)
			if !ok {
				// 2. Cache Miss: Fetch from DB
				var err error
				perms, err = m.resolver.GetPermissionsForRole(ctx, role)
				if err != nil {
					// Log error but don't crash; just treat as no permissions
					continue
				}
				// 3. Update Cache
				m.setCachedPermissions(role, perms)
			}

			// 4. Flatten into Map
			for _, p := range perms {
				permMap[p] = true
			}
		}

		// Inject into Context
		ctx = context.WithValue(ctx, PermissionsKey, permMap)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// RequirePermission creates a handler guard for specific routes
func RequirePermission(perm string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !HasPermission(r.Context(), perm) {
				http.Error(w, "forbidden: missing permission "+perm, http.StatusForbidden)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- Cache Helpers (Simple Concurrent Map) ---

func (m *RBACMiddleware) getCachedPermissions(role string) ([]string, bool) {
	val, ok := m.cache.Load(role)
	if !ok {
		return nil, false
	}
	// In a real system, you'd check TTL expiration here
	return val.([]string), true
}

func (m *RBACMiddleware) setCachedPermissions(role string, perms []string) {
	m.cache.Store(role, perms)
}

// --- Public Accessor ---

// HasPermission checks if the current user has the specific permission
func HasPermission(ctx context.Context, perm string) bool {
	// 1. Check if "saas-admin" (God mode)
	roles := GetRoles(ctx)
	for _, r := range roles {
		if r == "saas-admin" {
			return true
		}
	}

	// 2. Check Granular Permissions
	perms, ok := ctx.Value(PermissionsKey).(map[string]bool)
	if !ok {
		return false
	}
	return perms[perm]
}
