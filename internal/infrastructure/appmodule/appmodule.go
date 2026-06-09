package appmodule

// Definition describes a module's identity and permissions.
type Definition struct {
	Code        string
	Name        string
	Description string
	Permissions []PermissionDef
}

// PermissionDef is a single permission declaration inside a Definition.
type PermissionDef struct {
	Key         string // e.g. "tenants.create"  (no module prefix)
	Description string
}

// Module is the interface every application module implements
// to expose its definition for permission sync.
type Module interface {
	GetDefinition() Definition
}

// ─── Builder ─────────────────────────────────────────────────────────────────

type Builder struct{ def Definition }

// NewBuilder starts a Definition builder.
func NewBuilder(code, name, description string) *Builder {
	return &Builder{def: Definition{Code: code, Name: name, Description: description}}
}

// AddPermission adds a single custom permission.
func (b *Builder) AddPermission(key, description string) *Builder {
	b.def.Permissions = append(b.def.Permissions, PermissionDef{Key: key, Description: description})
	return b
}

// AddResource generates CRUD-style permissions for a named resource.
// Example: AddResource("users", "User", CRUD...) generates users.list, users.create, …
func (b *Builder) AddResource(resource, displayName string, actions ...string) *Builder {
	for _, a := range actions {
		b.def.Permissions = append(b.def.Permissions, PermissionDef{
			Key:         resource + "." + a,
			Description: a + " " + displayName,
		})
	}
	return b
}

// Build returns the completed Definition.
func (b *Builder) Build() Definition { return b.def }

// ─── Action Constants ─────────────────────────────────────────────────────────

const (
	ActionList   = "list"
	ActionCreate = "create"
	ActionView   = "view"
	ActionUpdate = "update"
	ActionDelete = "delete"
)

// CRUD is a convenience slice for AddResource calls.
var CRUD = []string{ActionList, ActionCreate, ActionView, ActionUpdate, ActionDelete}

// ─── ResourcePerms ────────────────────────────────────────────────────────────

// ResourcePerms provides typed, fully-qualified permission strings for a resource.
// Usage:  identity.Permissions.Users.Create  →  "identity.users.create"
type ResourcePerms struct {
	List   string
	Create string
	View   string
	Update string
	Delete string
}

// NewResourcePerms builds a ResourcePerms for module + resource.
func NewResourcePerms(module, resource string) ResourcePerms {
	p := module + "." + resource + "."
	return ResourcePerms{
		List:   p + ActionList,
		Create: p + ActionCreate,
		View:   p + ActionView,
		Update: p + ActionUpdate,
		Delete: p + ActionDelete,
	}
}
