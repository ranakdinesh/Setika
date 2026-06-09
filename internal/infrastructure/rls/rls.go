package rls

type contextKey string

const (
	// TenantIDKey is used to store the Tenant ID in the context
	TenantIDKey contextKey = "tenant_id"

	// IsSuperUserKey is used to flag a request as system-level (bypassing RLS)
	IsSuperUserKey contextKey = "is_super_user"
)
