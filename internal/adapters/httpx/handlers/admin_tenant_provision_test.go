package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/ranakdinesh/spur-identity/adapters/http/httputil"
)

func TestHasAssistedTenantProvisionAccess(t *testing.T) {
	if !hasAssistedTenantProvisionAccess(httputil.SetSuperAdmin(context.Background(), true)) {
		t.Fatal("super admin should be allowed")
	}
	if !hasAssistedTenantProvisionAccess(httputil.SetPermissions(context.Background(), []string{"identity.tenants.create"})) {
		t.Fatal("platform tenant create permission should be allowed")
	}
	if hasAssistedTenantProvisionAccess(httputil.SetPermissions(context.Background(), []string{"identity.tenants.list"})) {
		t.Fatal("read-only tenant permission should not be allowed")
	}
	if hasAssistedTenantProvisionAccess(context.Background()) {
		t.Fatal("anonymous context should not be allowed")
	}
}

func TestAdminProvisionTenantRejectsUnauthorizedRequest(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/admin/tenants/provision", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	(&Handler{}).AdminProvisionTenant(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestNormalizeAssistedProvisionRequestValidDefaults(t *testing.T) {
	planID := uuid.New()
	input, err := normalizeAssistedProvisionRequest(adminProvisionTenantRequest{
		CompanyName:       "Example Pvt Ltd",
		Subdomain:         "Example",
		EmployeeEstimate:  50,
		AdminFirstName:    "Tenant",
		AdminLastName:     "Admin",
		AdminEmail:        "Admin@Example.Test",
		AdminMobile:       "(999) 999-9999",
		PlanID:            planID.String(),
		TemporaryPassword: "local-demo-password",
	})
	if err != nil {
		t.Fatalf("normalizeAssistedProvisionRequest returned error: %v", err)
	}
	if input.Subdomain != "example" {
		t.Fatalf("subdomain = %q, want example", input.Subdomain)
	}
	if input.AdminEmail != "admin@example.test" {
		t.Fatalf("email = %q, want lower-cased email", input.AdminEmail)
	}
	if input.AdminMobile != "9999999999" {
		t.Fatalf("mobile = %q, want normalized mobile", input.AdminMobile)
	}
	if input.TrialDays != defaultAssistedProvisionTrialDays {
		t.Fatalf("trial days = %d, want %d", input.TrialDays, defaultAssistedProvisionTrialDays)
	}
	if input.Country != "IN" || input.Timezone != "Asia/Kolkata" {
		t.Fatalf("unexpected locale defaults: country=%q timezone=%q", input.Country, input.Timezone)
	}
	if input.BillingMode != "manual_billing" || input.PaymentMethodStatus != "manual_billing" {
		t.Fatalf("unexpected billing defaults: billing=%q payment=%q", input.BillingMode, input.PaymentMethodStatus)
	}
}

func TestNormalizeAssistedProvisionRequestRejectsInvalidInputs(t *testing.T) {
	planID := uuid.New().String()
	base := adminProvisionTenantRequest{
		CompanyName:       "Example Pvt Ltd",
		Subdomain:         "example",
		AdminFirstName:    "Tenant",
		AdminLastName:     "Admin",
		AdminEmail:        "admin@example.test",
		AdminMobile:       "9999999999",
		PlanID:            planID,
		TemporaryPassword: "local-demo-password",
	}
	tests := []struct {
		name string
		mut  func(*adminProvisionTenantRequest)
		want string
	}{
		{name: "missing company", mut: func(r *adminProvisionTenantRequest) { r.CompanyName = "" }, want: "company_name"},
		{name: "missing admin name", mut: func(r *adminProvisionTenantRequest) { r.AdminFirstName = "" }, want: "admin_first_name"},
		{name: "bad email", mut: func(r *adminProvisionTenantRequest) { r.AdminEmail = "not-email" }, want: "admin_email"},
		{name: "reserved subdomain", mut: func(r *adminProvisionTenantRequest) { r.Subdomain = "admin" }, want: "reserved"},
		{name: "confusing subdomain", mut: func(r *adminProvisionTenantRequest) { r.Subdomain = "example-pvt-ltd" }, want: "not allowed"},
		{name: "negative employees", mut: func(r *adminProvisionTenantRequest) { r.EmployeeEstimate = -1 }, want: "employee_estimate"},
		{name: "negative trial", mut: func(r *adminProvisionTenantRequest) { r.TrialDays = -1 }, want: "trial_days"},
		{name: "missing plan", mut: func(r *adminProvisionTenantRequest) { r.PlanID = "" }, want: "plan_id"},
		{name: "missing temp password", mut: func(r *adminProvisionTenantRequest) { r.TemporaryPassword = "" }, want: "temporary_password"},
		{name: "short temp password", mut: func(r *adminProvisionTenantRequest) { r.TemporaryPassword = "short" }, want: "temporary_password"},
		{name: "unsupported billing", mut: func(r *adminProvisionTenantRequest) { r.BillingMode = "card" }, want: "manual_billing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := base
			tt.mut(&req)
			_, err := normalizeAssistedProvisionRequest(req)
			if err == nil {
				t.Fatal("expected validation error")
			}
			if !errors.Is(err, errAssistedProvisionInvalid) {
				t.Fatalf("error should wrap errAssistedProvisionInvalid, got %v", err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}
