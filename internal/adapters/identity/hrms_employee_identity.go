package identity

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/uuid"
	hrmsdomain "github.com/ranakdinesh/spur-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/spur-hrms/core/ports"
	"github.com/ranakdinesh/spur-hrms/pkg/permissions"
	identitymodule "github.com/ranakdinesh/spur-identity"
)

type EmployeeIdentityAdapter struct {
	registration any
	auth         any
	rbac         any
}

func NewEmployeeIdentityAdapter(module *identitymodule.Module) (*EmployeeIdentityAdapter, error) {
	if module == nil || module.Services == nil {
		return nil, errors.New("identity module is not configured")
	}
	if module.Services.RegistrationService == nil {
		return nil, errors.New("identity registration service is not configured")
	}
	if module.Services.RBACService == nil {
		return nil, errors.New("identity rbac service is not configured")
	}
	if module.Services.AuthService == nil {
		return nil, errors.New("identity auth service is not configured")
	}
	return &EmployeeIdentityAdapter{
		registration: module.Services.RegistrationService,
		auth:         module.Services.AuthService,
		rbac:         module.Services.RBACService,
	}, nil
}

func (a *EmployeeIdentityAdapter) CheckEmployeeIdentityAvailability(ctx context.Context, cmd hrmsports.EmployeeIdentityAvailabilityCommand) error {
	result, err := a.callRegistration(ctx, "CheckAvailability", func(v reflect.Value) {
		setStringField(v, "Email", cmd.Email)
		setStringField(v, "Mobile", cmd.Mobile)
	})
	if err != nil {
		return err
	}
	if !boolPtrFieldAvailable(result, "EmailAvailable") {
		return fmt.Errorf("email already registered")
	}
	if !boolPtrFieldAvailable(result, "MobileAvailable") {
		return fmt.Errorf("mobile already registered")
	}
	return nil
}

func (a *EmployeeIdentityAdapter) CreateEmployeeIdentity(ctx context.Context, cmd hrmsports.CreateEmployeeIdentityCommand) (*hrmsports.EmployeeIdentity, error) {
	roleCodes, err := identityRoleCodes(cmd.Role)
	if err != nil {
		return nil, err
	}
	for _, roleCode := range roleCodes {
		if _, err := a.findOrCreateRoleIDByCode(ctx, cmd.TenantID, roleCode); err != nil {
			return nil, err
		}
	}
	result, err := a.callRegistration(ctx, "CreateUser", func(v reflect.Value) {
		setUUIDField(v, "TenantID", cmd.TenantID)
		setStringField(v, "FirstName", cmd.FirstName)
		setStringField(v, "LastName", cmd.LastName)
		setStringField(v, "Email", cmd.Email)
		setStringField(v, "Mobile", cmd.Mobile)
		setStringField(v, "Password", cmd.Password)
		setStringSliceField(v, "Roles", roleCodes)
		setBoolField(v, "IsSuperAdmin", false)
		setUUIDPtrField(v, "CreatedByUserID", cmd.ActorID)
	})
	if err != nil {
		return nil, err
	}
	userID, ok := uuidField(result, "ID")
	if !ok || userID == uuid.Nil {
		return nil, errors.New("identity user id is missing")
	}
	return &hrmsports.EmployeeIdentity{UserID: userID}, nil
}

func (a *EmployeeIdentityAdapter) AssignEmployeeRole(ctx context.Context, cmd hrmsports.AssignEmployeeRoleCommand) error {
	roleCodes, err := identityRoleCodes(cmd.Role)
	if err != nil {
		return err
	}
	for _, roleCode := range roleCodes {
		roleID, err := a.findOrCreateRoleIDByCode(ctx, cmd.TenantID, roleCode)
		if err != nil {
			return err
		}
		if _, err = a.callRegistrationWithValues(ctx, "AssignRoleToUser", reflect.ValueOf(cmd.UserID), reflect.ValueOf(cmd.TenantID), reflect.ValueOf(roleID)); err != nil {
			return err
		}
	}
	return nil
}

func (a *EmployeeIdentityAdapter) DeactivateEmployeeIdentity(ctx context.Context, cmd hrmsports.DeactivateEmployeeIdentityCommand) error {
	_, err := a.callRegistrationWithValues(ctx, "DeactivateUser", reflect.ValueOf(cmd.UserID), reflect.ValueOf(cmd.TenantID))
	return err
}

func (a *EmployeeIdentityAdapter) SendEmployeePasswordReset(ctx context.Context, cmd hrmsports.EmployeeCredentialResetCommand) error {
	email := strings.TrimSpace(cmd.Email)
	if email == "" {
		return hrmsdomain.ErrEmployeeCredentialTarget
	}
	_, err := callMethod(a.auth, "RequestPasswordReset", reflect.ValueOf(ctx), reflect.ValueOf(email))
	return err
}

func (a *EmployeeIdentityAdapter) SetEmployeeTemporaryPassword(ctx context.Context, cmd hrmsports.EmployeeTemporaryPasswordCommand) error {
	if strings.TrimSpace(cmd.TemporaryPassword) == "" {
		return hrmsdomain.ErrInvalidTemporaryPassword
	}
	if _, err := a.callRegistrationWithValues(ctx, "UpdateUserPassword", reflect.ValueOf(cmd.UserID), reflect.ValueOf(cmd.TenantID), reflect.ValueOf(strings.TrimSpace(cmd.TemporaryPassword))); err != nil {
		return err
	}
	return a.SendEmployeePasswordReset(ctx, hrmsports.EmployeeCredentialResetCommand{
		TenantID: cmd.TenantID, UserID: cmd.UserID, Email: cmd.Email, EmployeeID: cmd.EmployeeID, Employee: cmd.Employee, EmployeeCode: cmd.EmployeeCode, ActorID: cmd.ActorID,
	})
}

func (a *EmployeeIdentityAdapter) findOrCreateRoleIDByCode(ctx context.Context, tenantID uuid.UUID, code string) (uuid.UUID, error) {
	roleID, found, err := a.findRoleIDByCode(ctx, tenantID, code)
	if err != nil || found {
		return roleID, err
	}
	role, err := a.instantiateRoleTemplateByCode(ctx, tenantID, code)
	if err != nil {
		return uuid.Nil, err
	}
	roleID, ok := uuidField(role, "ID")
	if !ok || roleID == uuid.Nil {
		return uuid.Nil, fmt.Errorf("identity role %s was instantiated without an id", code)
	}
	return roleID, nil
}

func (a *EmployeeIdentityAdapter) findRoleIDByCode(ctx context.Context, tenantID uuid.UUID, code string) (uuid.UUID, bool, error) {
	results, err := callMethod(a.rbac, "ListRoles", reflect.ValueOf(ctx), reflect.ValueOf(tenantID))
	if err != nil {
		return uuid.Nil, false, err
	}
	if len(results) == 0 || isNil(results[0]) {
		return uuid.Nil, false, nil
	}
	roles := results[0]
	for i := 0; i < roles.Len(); i++ {
		role := indirect(roles.Index(i))
		if !role.IsValid() {
			continue
		}
		if strings.EqualFold(stringPtrField(role, "Code"), code) {
			if roleID, ok := uuidField(role, "ID"); ok {
				return roleID, true, nil
			}
		}
	}
	return uuid.Nil, false, nil
}

func (a *EmployeeIdentityAdapter) instantiateRoleTemplateByCode(ctx context.Context, tenantID uuid.UUID, code string) (reflect.Value, error) {
	if err := a.ensureHRMSModuleEnabled(ctx, tenantID); err != nil {
		return reflect.Value{}, err
	}
	results, err := callMethod(a.rbac, "ListTenantRoleTemplates", reflect.ValueOf(ctx), reflect.ValueOf(tenantID))
	if err != nil {
		return reflect.Value{}, err
	}
	if len(results) == 0 || isNil(results[0]) {
		return reflect.Value{}, fmt.Errorf("identity role template %s not found", code)
	}
	templates := results[0]
	for i := 0; i < templates.Len(); i++ {
		template := indirect(templates.Index(i))
		if !template.IsValid() || !strings.EqualFold(stringField(template, "Code"), code) {
			continue
		}
		templateID, ok := uuidField(template, "ID")
		if !ok || templateID == uuid.Nil {
			return reflect.Value{}, fmt.Errorf("identity role template %s has no id", code)
		}
		created, err := callMethod(a.rbac, "InstantiateRoleTemplate", reflect.ValueOf(ctx), reflect.ValueOf(tenantID), reflect.ValueOf(templateID))
		if err != nil {
			return reflect.Value{}, err
		}
		if len(created) == 0 || isNil(created[0]) {
			return reflect.Value{}, fmt.Errorf("identity role template %s did not create a role", code)
		}
		return created[0], nil
	}
	return reflect.Value{}, fmt.Errorf("identity role template %s not found", code)
}

func (a *EmployeeIdentityAdapter) ensureHRMSModuleEnabled(ctx context.Context, tenantID uuid.UUID) error {
	tenantModules, err := callMethod(a.rbac, "ListTenantModules", reflect.ValueOf(ctx), reflect.ValueOf(tenantID))
	if err != nil {
		return err
	}
	if hasModuleCode(tenantModules, permissions.ModuleCode) {
		return nil
	}
	allModules, err := callMethod(a.rbac, "ListModules", reflect.ValueOf(ctx))
	if err != nil {
		return err
	}
	moduleID, ok := findModuleIDByCode(allModules, permissions.ModuleCode)
	if !ok {
		return fmt.Errorf("identity module %s is not registered", permissions.ModuleCode)
	}
	_, err = callMethod(a.rbac, "EnableModuleForTenant", reflect.ValueOf(ctx), reflect.ValueOf(tenantID), reflect.ValueOf(moduleID))
	return err
}

func (a *EmployeeIdentityAdapter) callRegistration(ctx context.Context, method string, fill func(reflect.Value)) (reflect.Value, error) {
	m := reflect.ValueOf(a.registration).MethodByName(method)
	if !m.IsValid() {
		return reflect.Value{}, fmt.Errorf("identity registration method %s is not available", method)
	}
	if m.Type().NumIn() < 2 {
		return reflect.Value{}, fmt.Errorf("identity registration method %s has invalid signature", method)
	}
	cmd := reflect.New(m.Type().In(1)).Elem()
	fill(cmd)
	results := m.Call([]reflect.Value{reflect.ValueOf(ctx), cmd})
	return parseResult(method, results)
}

func (a *EmployeeIdentityAdapter) callRegistrationWithValues(ctx context.Context, method string, values ...reflect.Value) (reflect.Value, error) {
	args := append([]reflect.Value{reflect.ValueOf(ctx)}, values...)
	results, err := callMethod(a.registration, method, args...)
	if err != nil || len(results) == 0 {
		return reflect.Value{}, err
	}
	return results[0], nil
}

func callMethod(target any, method string, args ...reflect.Value) ([]reflect.Value, error) {
	m := reflect.ValueOf(target).MethodByName(method)
	if !m.IsValid() {
		return nil, fmt.Errorf("identity method %s is not available", method)
	}
	results := m.Call(args)
	if len(results) > 0 {
		if err := errorFromLastResult(results); err != nil {
			return results[:len(results)-1], err
		}
		if isErrorType(results[len(results)-1]) {
			return results[:len(results)-1], nil
		}
	}
	return results, nil
}

func parseResult(method string, results []reflect.Value) (reflect.Value, error) {
	if len(results) == 0 {
		return reflect.Value{}, nil
	}
	if err := errorFromLastResult(results); err != nil {
		if len(results) == 1 {
			return reflect.Value{}, err
		}
		return results[0], err
	}
	if isErrorType(results[len(results)-1]) {
		if len(results) == 1 {
			return reflect.Value{}, nil
		}
		return results[0], nil
	}
	if len(results) == 1 {
		return results[0], nil
	}
	return reflect.Value{}, fmt.Errorf("identity method %s returned unexpected result count", method)
}

func identityRoleCode(role string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "", hrmsdomain.RoleEmployee:
		return "EMPLOYEE", nil
	case hrmsdomain.RoleHR:
		return "HR", nil
	case hrmsdomain.RoleManager:
		return "MANAGER", nil
	case hrmsdomain.RoleTenant:
		return "TENANT_ADMIN", nil
	case hrmsdomain.RoleApplicant:
		return "APPLICANT", nil
	default:
		return "", fmt.Errorf("unsupported employee identity role %q", role)
	}
}

func identityRoleCodes(role string) ([]string, error) {
	roleCode, err := identityRoleCode(role)
	if err != nil {
		return nil, err
	}
	if roleCode == "EMPLOYEE" || roleCode == "APPLICANT" {
		return []string{roleCode}, nil
	}
	return []string{"EMPLOYEE", roleCode}, nil
}

func setStringField(v reflect.Value, name string, value string) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.String {
		field.SetString(value)
	}
}

func setStringSliceField(v reflect.Value, name string, value []string) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Slice {
		field.Set(reflect.ValueOf(value))
	}
}

func setBoolField(v reflect.Value, name string, value bool) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Kind() == reflect.Bool {
		field.SetBool(value)
	}
}

func setUUIDField(v reflect.Value, name string, value uuid.UUID) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf(uuid.UUID{}) {
		field.Set(reflect.ValueOf(value))
	}
}

func setUUIDPtrField(v reflect.Value, name string, value *uuid.UUID) {
	field := v.FieldByName(name)
	if field.IsValid() && field.CanSet() && field.Type() == reflect.TypeOf((*uuid.UUID)(nil)) {
		field.Set(reflect.ValueOf(value))
	}
}

func boolPtrFieldAvailable(v reflect.Value, name string) bool {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Pointer || field.IsNil() {
		return true
	}
	return field.Elem().Bool()
}

func uuidField(v reflect.Value, name string) (uuid.UUID, bool) {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() || field.Type() != reflect.TypeOf(uuid.UUID{}) {
		return uuid.Nil, false
	}
	return field.Interface().(uuid.UUID), true
}

func stringPtrField(v reflect.Value, name string) string {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.Pointer || field.IsNil() {
		return ""
	}
	return field.Elem().String()
}

func stringField(v reflect.Value, name string) string {
	v = indirect(v)
	field := v.FieldByName(name)
	if !field.IsValid() || field.Kind() != reflect.String {
		return ""
	}
	return field.String()
}

func hasModuleCode(results []reflect.Value, code string) bool {
	_, ok := findModuleIDByCode(results, code)
	return ok
}

func findModuleIDByCode(results []reflect.Value, code string) (uuid.UUID, bool) {
	if len(results) == 0 || isNil(results[0]) {
		return uuid.Nil, false
	}
	modules := results[0]
	for i := 0; i < modules.Len(); i++ {
		module := indirect(modules.Index(i))
		if !module.IsValid() || !strings.EqualFold(stringField(module, "Code"), code) {
			continue
		}
		moduleID, ok := uuidField(module, "ID")
		if !ok || moduleID == uuid.Nil {
			return uuid.Nil, false
		}
		return moduleID, true
	}
	return uuid.Nil, false
}

func indirect(v reflect.Value) reflect.Value {
	for v.IsValid() && (v.Kind() == reflect.Interface || v.Kind() == reflect.Pointer) {
		if v.IsNil() {
			return reflect.Value{}
		}
		v = v.Elem()
	}
	return v
}

func isNil(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func errorFromLastResult(results []reflect.Value) error {
	if len(results) == 0 {
		return nil
	}
	last := results[len(results)-1]
	if !isErrorType(last) || last.IsNil() {
		return nil
	}
	return last.Interface().(error)
}

func isErrorType(v reflect.Value) bool {
	errorType := reflect.TypeOf((*error)(nil)).Elem()
	return v.IsValid() && v.Type().Implements(errorType)
}
