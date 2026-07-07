package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	hrmsdomain "github.com/ranakdinesh/spur-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/spur-hrms/core/ports"
	"github.com/ranakdinesh/spur-identity/adapters/http/httputil"
	identitydb "github.com/ranakdinesh/spur-identity/adapters/postgres/db"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultAssistedProvisionTrialDays = 30
	assistedProvisionTempPasswordMin  = 12
)

var assistedTenantSubdomainPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

type adminProvisionTenantRequest struct {
	CompanyName         string `json:"company_name"`
	LegalName           string `json:"legal_name"`
	Subdomain           string `json:"subdomain"`
	EmployeeEstimate    int32  `json:"employee_estimate"`
	Country             string `json:"country"`
	Timezone            string `json:"timezone"`
	AdminFirstName      string `json:"admin_first_name"`
	AdminLastName       string `json:"admin_last_name"`
	AdminEmail          string `json:"admin_email"`
	AdminMobile         string `json:"admin_mobile"`
	PlanID              string `json:"plan_id"`
	TrialDays           int32  `json:"trial_days"`
	BillingMode         string `json:"billing_mode"`
	PaymentMethodStatus string `json:"payment_method_status"`
	SendInvite          bool   `json:"send_invite"`
	TemporaryPassword   string `json:"temporary_password"`
}

type adminProvisionTenantResponse struct {
	TenantID           uuid.UUID  `json:"tenant_id"`
	TenantName         string     `json:"tenant_name"`
	Subdomain          string     `json:"subdomain"`
	TenantURL          string     `json:"tenant_url"`
	AdminUserID        uuid.UUID  `json:"admin_user_id"`
	AdminEmail         string     `json:"admin_email"`
	SubscriptionID     uuid.UUID  `json:"subscription_id"`
	PlanCode           string     `json:"plan_code"`
	TrialEndsAt        *time.Time `json:"trial_ends_at,omitempty"`
	ProvisioningStatus string     `json:"provisioning_status"`
	InviteStatus       string     `json:"invite_status"`
}

type assistedProvisionInput struct {
	CompanyName         string
	LegalName           string
	Subdomain           string
	EmployeeEstimate    int32
	Country             string
	Timezone            string
	AdminFirstName      string
	AdminLastName       string
	AdminEmail          string
	AdminMobile         string
	PlanID              uuid.UUID
	TrialDays           int32
	BillingMode         string
	PaymentMethodStatus string
	SendInvite          bool
	TemporaryPassword   string
}

func (h *Handler) AdminProvisionTenant(w http.ResponseWriter, r *http.Request) {
	if !hasAssistedTenantProvisionAccess(r.Context()) {
		writeError(w, http.StatusForbidden, "platform tenant provisioning access required")
		return
	}
	if h == nil || h.db == nil || h.hrms == nil || h.hrms.Services == nil || h.hrms.Services.Hrms == nil {
		writeError(w, http.StatusInternalServerError, "admin tenant provision handler is not configured")
		return
	}

	var req adminProvisionTenantRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	input, err := normalizeAssistedProvisionRequest(req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	result, err := h.provisionAssistedTenant(r.Context(), input)
	if err != nil {
		h.logSignupError(r.Context(), "assisted tenant provision", err)
		status := http.StatusInternalServerError
		if errors.Is(err, errAssistedProvisionConflict) {
			status = http.StatusConflict
		} else if errors.Is(err, errAssistedProvisionInvalid) {
			status = http.StatusBadRequest
		}
		writeError(w, status, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result)
}

var (
	errAssistedProvisionInvalid  = errors.New("assisted tenant provision request is invalid")
	errAssistedProvisionConflict = errors.New("assisted tenant provision conflict")
)

func hasAssistedTenantProvisionAccess(ctx context.Context) bool {
	if httputil.IsSuperAdmin(ctx) {
		return true
	}
	for _, permission := range httputil.GetPermissions(ctx) {
		switch strings.TrimSpace(permission) {
		case "identity.tenants.create", "platform.tenants.create", "platform.tenants.provision":
			return true
		}
	}
	return false
}

func normalizeAssistedProvisionRequest(req adminProvisionTenantRequest) (assistedProvisionInput, error) {
	input := assistedProvisionInput{
		CompanyName:         strings.TrimSpace(req.CompanyName),
		LegalName:           strings.TrimSpace(req.LegalName),
		Subdomain:           hrmsdomain.NormalizeSubdomain(req.Subdomain),
		EmployeeEstimate:    req.EmployeeEstimate,
		Country:             strings.ToUpper(strings.TrimSpace(req.Country)),
		Timezone:            strings.TrimSpace(req.Timezone),
		AdminFirstName:      strings.TrimSpace(req.AdminFirstName),
		AdminLastName:       strings.TrimSpace(req.AdminLastName),
		AdminEmail:          strings.ToLower(strings.TrimSpace(req.AdminEmail)),
		AdminMobile:         normalizeAssistedProvisionMobile(req.AdminMobile),
		TrialDays:           req.TrialDays,
		BillingMode:         strings.ToLower(strings.TrimSpace(req.BillingMode)),
		PaymentMethodStatus: strings.ToLower(strings.TrimSpace(req.PaymentMethodStatus)),
		SendInvite:          req.SendInvite,
		TemporaryPassword:   strings.TrimSpace(req.TemporaryPassword),
	}
	if input.CompanyName == "" {
		return input, fmt.Errorf("%w: company_name is required", errAssistedProvisionInvalid)
	}
	if input.LegalName == "" {
		input.LegalName = input.CompanyName
	}
	if input.AdminFirstName == "" || input.AdminLastName == "" {
		return input, fmt.Errorf("%w: admin_first_name and admin_last_name are required", errAssistedProvisionInvalid)
	}
	if input.AdminEmail == "" {
		return input, fmt.Errorf("%w: admin_email is required", errAssistedProvisionInvalid)
	}
	if _, err := mail.ParseAddress(input.AdminEmail); err != nil {
		return input, fmt.Errorf("%w: admin_email is invalid", errAssistedProvisionInvalid)
	}
	if input.AdminMobile == "" {
		return input, fmt.Errorf("%w: admin_mobile is required", errAssistedProvisionInvalid)
	}
	if input.Subdomain == "" {
		return input, fmt.Errorf("%w: subdomain is required", errAssistedProvisionInvalid)
	}
	if !assistedTenantSubdomainPattern.MatchString(input.Subdomain) {
		return input, fmt.Errorf("%w: subdomain is invalid", errAssistedProvisionInvalid)
	}
	if hrmsdomain.IsReservedTenantSubdomain(input.Subdomain) || isReservedTenantLoginSubdomain(input.Subdomain) {
		return input, fmt.Errorf("%w: subdomain is reserved", errAssistedProvisionInvalid)
	}
	if hrmsdomain.HasTenantSubdomainBusinessSuffix(input.Subdomain) || hrmsdomain.TenantSubdomainCollisionKey(input.Subdomain) == "" {
		return input, fmt.Errorf("%w: subdomain is not allowed", errAssistedProvisionInvalid)
	}
	if input.EmployeeEstimate < 0 {
		return input, fmt.Errorf("%w: employee_estimate cannot be negative", errAssistedProvisionInvalid)
	}
	if input.Country == "" {
		input.Country = "IN"
	}
	if len(input.Country) != 2 {
		return input, fmt.Errorf("%w: country must be an ISO-3166 alpha-2 code", errAssistedProvisionInvalid)
	}
	if input.Timezone == "" {
		input.Timezone = "Asia/Kolkata"
	}
	if _, err := time.LoadLocation(input.Timezone); err != nil {
		return input, fmt.Errorf("%w: timezone is invalid", errAssistedProvisionInvalid)
	}
	if input.BillingMode == "" {
		input.BillingMode = "manual_billing"
	}
	if input.PaymentMethodStatus == "" {
		input.PaymentMethodStatus = input.BillingMode
	}
	if input.BillingMode != "manual_billing" {
		return input, fmt.Errorf("%w: billing_mode must be manual_billing for assisted MVP", errAssistedProvisionInvalid)
	}
	if input.PaymentMethodStatus != "manual_billing" {
		return input, fmt.Errorf("%w: payment_method_status must be manual_billing for assisted MVP", errAssistedProvisionInvalid)
	}
	if input.TrialDays < 0 {
		return input, fmt.Errorf("%w: trial_days cannot be negative", errAssistedProvisionInvalid)
	}
	if input.TrialDays == 0 {
		input.TrialDays = defaultAssistedProvisionTrialDays
	}
	planID, err := uuid.Parse(strings.TrimSpace(req.PlanID))
	if err != nil || planID == uuid.Nil {
		return input, fmt.Errorf("%w: plan_id is required", errAssistedProvisionInvalid)
	}
	input.PlanID = planID
	if input.TemporaryPassword != "" && len(input.TemporaryPassword) < assistedProvisionTempPasswordMin {
		return input, fmt.Errorf("%w: temporary_password must be at least %d characters", errAssistedProvisionInvalid, assistedProvisionTempPasswordMin)
	}
	if input.TemporaryPassword == "" {
		return input, fmt.Errorf("%w: temporary_password is required until tenant-admin invite flow is available", errAssistedProvisionInvalid)
	}
	if strings.EqualFold(os.Getenv("APP_ENV"), "production") && os.Getenv("SETIKA_ALLOW_ASSISTED_TEMP_PASSWORD") != "1" {
		return input, fmt.Errorf("%w: temporary_password assisted provisioning is disabled in production", errAssistedProvisionInvalid)
	}
	return input, nil
}

func (h *Handler) provisionAssistedTenant(ctx context.Context, input assistedProvisionInput) (*adminProvisionTenantResponse, error) {
	if err := h.validateAssistedProvisionMasterData(ctx, input); err != nil {
		return nil, err
	}
	if err := h.ensureAssistedTenantProvisionUniqueness(ctx, input); err != nil {
		return nil, err
	}

	var planCode string
	var planEmployeeLimit int32
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		plan, err := h.hrms.Services.Hrms.GetSubscriptionPlan(systemCtx, input.PlanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: selected plan not found", errAssistedProvisionInvalid)
			}
			return err
		}
		if !plan.IsActive || plan.Inactive {
			return fmt.Errorf("%w: selected plan is inactive", errAssistedProvisionInvalid)
		}
		planCode = plan.Code
		planEmployeeLimit = plan.EmployeeLimit
		return nil
	}); err != nil {
		return nil, err
	}

	actorID := httputil.GetUserID(ctx)
	var actorIDPtr *uuid.UUID
	if actorID != uuid.Nil {
		actorIDPtr = &actorID
	}
	trialEndsAt := time.Now().UTC().AddDate(0, 0, int(input.TrialDays))
	var trialEndsAtPtr *time.Time
	if input.TrialDays > 0 {
		trialEndsAtPtr = &trialEndsAt
	}
	passwordHash, inviteStatus, err := passwordHashForAssistedProvision(input)
	if err != nil {
		return nil, err
	}

	tenantID, adminUserID, err := h.createAssistedIdentityTenant(ctx, input, assistedIdentityTenantInput{
		PlanCode:        planCode,
		TrialEndsAt:     trialEndsAtPtr,
		PasswordHash:    passwordHash,
		CreatedByUserID: actorIDPtr,
	})
	if err != nil {
		return nil, err
	}

	tenantURL := "https://" + h.tenantHost(input.Subdomain)
	adminName := strings.TrimSpace(input.AdminFirstName + " " + input.AdminLastName)
	activationCode, err := generateActivationCode(input.Subdomain)
	if err != nil {
		return nil, err
	}
	if _, err := h.hrms.Services.Hrms.ProvisionTenant(ctx, hrmsports.ProvisionTenantCommand{
		TenantID:             tenantID,
		CompanyName:          input.CompanyName,
		Subdomain:            input.Subdomain,
		MobileActivationCode: activationCode,
		AdminEmail:           &input.AdminEmail,
		AdminName:            &adminName,
		TenantURL:            &tenantURL,
		ActorID:              actorIDPtr,
	}); err != nil {
		return nil, err
	}

	maxEmployees := input.EmployeeEstimate
	if maxEmployees == 0 && planEmployeeLimit > 0 {
		maxEmployees = planEmployeeLimit
	}
	status := hrmsdomain.SubscriptionStatusActive
	endDate := ""
	if trialEndsAtPtr != nil {
		status = hrmsdomain.SubscriptionStatusTrialing
		endDate = trialEndsAt.Format("2006-01-02")
	}

	var subscriptionID uuid.UUID
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		subscription, err := h.hrms.Services.Hrms.CreateTenantSubscription(systemCtx, hrmsports.TenantSubscriptionCommand{
			TenantID:     tenantID,
			PlanID:       &input.PlanID,
			StartDate:    time.Now().UTC().Format("2006-01-02"),
			EndDate:      endDate,
			Status:       status,
			MaxEmployees: maxEmployees,
			ActorID:      actorIDPtr,
		})
		if err != nil {
			return err
		}
		subscriptionID = subscription.ID
		return nil
	}); err != nil {
		return nil, err
	}

	if err := h.insertAssistedProvisionAuditEvent(ctx, actorIDPtr, tenantID, adminUserID, input, planCode); err != nil && h.log != nil {
		h.log.Error(ctx).Err(err).Str("operation", "assisted tenant provision audit").Msg("audit event insert failed")
	}

	return &adminProvisionTenantResponse{
		TenantID:           tenantID,
		TenantName:         input.CompanyName,
		Subdomain:          input.Subdomain,
		TenantURL:          tenantURL,
		AdminUserID:        adminUserID,
		AdminEmail:         input.AdminEmail,
		SubscriptionID:     subscriptionID,
		PlanCode:           planCode,
		TrialEndsAt:        trialEndsAtPtr,
		ProvisioningStatus: "completed",
		InviteStatus:       inviteStatus,
	}, nil
}

func (h *Handler) validateAssistedProvisionMasterData(ctx context.Context, input assistedProvisionInput) error {
	var countryExists bool
	if err := h.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM platform.countries
			WHERE iso_alpha2 = $1
			  AND is_active = TRUE
		)
	`, input.Country).Scan(&countryExists); err != nil {
		return fmt.Errorf("validate country master data: %w", err)
	}
	if !countryExists {
		return fmt.Errorf("%w: country is unsupported", errAssistedProvisionInvalid)
	}

	var timezoneExists bool
	if err := h.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM platform.timezones
			WHERE id = $1
			  AND is_active = TRUE
		)
	`, input.Timezone).Scan(&timezoneExists); err != nil {
		return fmt.Errorf("validate timezone master data: %w", err)
	}
	if !timezoneExists {
		return fmt.Errorf("%w: timezone is unsupported", errAssistedProvisionInvalid)
	}
	return nil
}

type assistedIdentityTenantInput struct {
	PlanCode                   string
	TrialEndsAt                *time.Time
	PasswordHash               string
	EmailVerificationTokenHash string
	CreatedByUserID            *uuid.UUID
}

func (h *Handler) createAssistedIdentityTenant(ctx context.Context, input assistedProvisionInput, tenantInput assistedIdentityTenantInput) (uuid.UUID, uuid.UUID, error) {
	tenantID := uuid.New()
	adminUserID := uuid.New()
	if err := identitydb.RunInTx(ctx, h.db, func(txCtx context.Context) error {
		tx := identitydb.GetTx(txCtx)
		if tx == nil {
			return errors.New("identity transaction is not available")
		}
		if _, err := tx.Exec(txCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			return err
		}
		if err := ensureAssistedIdentityUserUnique(txCtx, input.AdminEmail, input.AdminMobile); err != nil {
			return err
		}
		if _, err := tx.Exec(txCtx, `
			INSERT INTO auth.tenants (id, name, kind, status, trial_ends_at, subscription_plan)
			VALUES ($1, $2, 'customer', 'active', $3, $4)
		`, tenantID, input.CompanyName, tenantInput.TrialEndsAt, tenantInput.PlanCode); err != nil {
			return err
		}
		if _, err := tx.Exec(txCtx, `
			INSERT INTO auth.users (
				id, tenant_id, first_name, last_name, email, mobile, password_hash,
				is_super_admin, authz_version, is_active, email_verified_at, mobile_verified_at,
				verification_grace_period_end, verified_at, is_locked, created_by_user_id
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7,
				false, 1, true, NOW(), NOW(),
				NOW(), NOW(), false, $8
			)
		`, adminUserID, tenantID, input.AdminFirstName, input.AdminLastName, input.AdminEmail, input.AdminMobile, tenantInput.PasswordHash, tenantInput.CreatedByUserID); err != nil {
			return err
		}
		if _, err := tx.Exec(txCtx, `
			INSERT INTO auth.user_profiles (
				user_id, tenant_id, display_name, default_dashboard_module, timezone, locale, metadata
			) VALUES (
				$1, $2, $3, 'hrms', $4, 'en-IN',
				jsonb_build_object('assisted_provision', true, 'country', $5::text, 'legal_name', $6::text)
			)
			ON CONFLICT (user_id) DO UPDATE
			SET tenant_id = EXCLUDED.tenant_id,
				display_name = EXCLUDED.display_name,
				default_dashboard_module = EXCLUDED.default_dashboard_module,
				timezone = EXCLUDED.timezone,
				locale = EXCLUDED.locale,
				metadata = auth.user_profiles.metadata || EXCLUDED.metadata,
				updated_at = NOW()
		`, adminUserID, tenantID, strings.TrimSpace(input.AdminFirstName+" "+input.AdminLastName), input.Timezone, input.Country, input.LegalName); err != nil {
			return err
		}
		tenantAdminRoleID, err := ensureAssistedTenantAdminRole(txCtx, tenantID)
		if err != nil {
			return err
		}
		if err := grantAssistedTenantBaselineModules(txCtx, tenantID, tenantInput.TrialEndsAt); err != nil {
			return err
		}
		if err := grantAssistedTenantAdminPermissions(txCtx, tenantAdminRoleID); err != nil {
			return err
		}
		if _, err := tx.Exec(txCtx, `
			INSERT INTO auth.user_roles (user_id, role_id)
			VALUES ($1, $2)
			ON CONFLICT DO NOTHING
		`, adminUserID, tenantAdminRoleID); err != nil {
			return err
		}
		if strings.TrimSpace(tenantInput.EmailVerificationTokenHash) != "" {
			if _, err := tx.Exec(txCtx, `
				INSERT INTO auth.verification_challenges (
					id, tenant_id, user_id, kind, token_hash, expires_at, created_at
				) VALUES (
					$1, $2, $3, 'email_verify', $4, NOW() + INTERVAL '15 minutes', NOW()
				)
			`, uuid.New(), tenantID, adminUserID, tenantInput.EmailVerificationTokenHash); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return uuid.Nil, uuid.Nil, err
	}
	return tenantID, adminUserID, nil
}

func (h *Handler) ensureAssistedTenantProvisionUniqueness(ctx context.Context, input assistedProvisionInput) error {
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		if _, err := h.hrms.Services.Hrms.ResolveTenantBySubdomain(systemCtx, input.Subdomain); err == nil {
			return fmt.Errorf("%w: subdomain already exists", errAssistedProvisionConflict)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	if err := identitydb.RunInTx(ctx, h.db, func(txCtx context.Context) error {
		tx := identitydb.GetTx(txCtx)
		if tx == nil {
			return errors.New("identity transaction is not available")
		}
		if _, err := tx.Exec(txCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			return err
		}
		return ensureAssistedIdentityUserUnique(txCtx, input.AdminEmail, input.AdminMobile)
	}); err != nil {
		return err
	}
	return nil
}

func (h *Handler) resumePartialSignupTenantProvision(ctx context.Context, input assistedProvisionInput) (*adminProvisionTenantResponse, error) {
	if err := h.validateAssistedProvisionMasterData(ctx, input); err != nil {
		return nil, err
	}
	tenantID, adminUserID, err := h.findPartialSignupIdentityTenant(ctx, input)
	if err != nil {
		return nil, err
	}

	var planCode string
	var planEmployeeLimit int32
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		plan, err := h.hrms.Services.Hrms.GetSubscriptionPlan(systemCtx, input.PlanID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("%w: selected plan not found", errAssistedProvisionInvalid)
			}
			return err
		}
		if !plan.IsActive || plan.Inactive {
			return fmt.Errorf("%w: selected plan is inactive", errAssistedProvisionInvalid)
		}
		planCode = plan.Code
		planEmployeeLimit = plan.EmployeeLimit
		return nil
	}); err != nil {
		return nil, err
	}

	if err := h.ensureResumeSubdomainIsSafe(ctx, tenantID, input.Subdomain); err != nil {
		return nil, err
	}

	trialEndsAt := time.Now().UTC().AddDate(0, 0, int(input.TrialDays))
	var trialEndsAtPtr *time.Time
	if input.TrialDays > 0 {
		trialEndsAtPtr = &trialEndsAt
	}
	tenantURL := "https://" + h.tenantHost(input.Subdomain)
	if err := h.ensureHRMSTenantProfile(ctx, tenantID, input, tenantURL); err != nil {
		return nil, err
	}

	subscriptionID, err := h.ensureSignupTenantSubscription(ctx, tenantID, input.PlanID, planEmployeeLimit, trialEndsAt)
	if err != nil {
		return nil, err
	}

	return &adminProvisionTenantResponse{
		TenantID:           tenantID,
		TenantName:         input.CompanyName,
		Subdomain:          input.Subdomain,
		TenantURL:          tenantURL,
		AdminUserID:        adminUserID,
		AdminEmail:         input.AdminEmail,
		SubscriptionID:     subscriptionID,
		PlanCode:           planCode,
		TrialEndsAt:        trialEndsAtPtr,
		ProvisioningStatus: "completed",
		InviteStatus:       "not_applicable",
	}, nil
}

func (h *Handler) findPartialSignupIdentityTenant(ctx context.Context, input assistedProvisionInput) (uuid.UUID, uuid.UUID, error) {
	var tenantID uuid.UUID
	var userID uuid.UUID
	err := identitydb.RunInTx(ctx, h.db, func(txCtx context.Context) error {
		tx := identitydb.GetTx(txCtx)
		if tx == nil {
			return errors.New("identity transaction is not available")
		}
		if _, err := tx.Exec(txCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			return err
		}
		return tx.QueryRow(txCtx, `
			SELECT u.tenant_id, u.id
			FROM auth.users u
			JOIN auth.tenants t ON t.id = u.tenant_id
			WHERE lower(u.email) = lower($1)
			  AND u.mobile = $2
			  AND lower(t.name) = lower($3)
			  AND t.kind = 'customer'
			  AND t.status = 'active'
			ORDER BY u.created_at DESC
			LIMIT 1
		`, input.AdminEmail, input.AdminMobile, input.CompanyName).Scan(&tenantID, &userID)
	})
	return tenantID, userID, err
}

func (h *Handler) ensureResumeSubdomainIsSafe(ctx context.Context, tenantID uuid.UUID, subdomain string) error {
	var ownerTenantID uuid.UUID
	err := h.db.QueryRow(ctx, `
		SELECT tenant_id
		FROM hrms.tenant_profiles
		WHERE subdomain = $1
	`, subdomain).Scan(&ownerTenantID)
	if err == nil {
		if ownerTenantID != tenantID {
			return fmt.Errorf("%w: subdomain already exists", errAssistedProvisionConflict)
		}
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	return fmt.Errorf("check signup resume subdomain: %w", err)
}

func (h *Handler) ensureHRMSTenantProfile(ctx context.Context, tenantID uuid.UUID, input assistedProvisionInput, tenantURL string) error {
	var existingSubdomain string
	err := h.db.QueryRow(ctx, `
		SELECT subdomain
		FROM hrms.tenant_profiles
		WHERE tenant_id = $1
	`, tenantID).Scan(&existingSubdomain)
	if err == nil {
		if existingSubdomain != input.Subdomain {
			return fmt.Errorf("%w: existing tenant profile uses a different subdomain", errAssistedProvisionConflict)
		}
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("check existing HRMS tenant profile: %w", err)
	}

	adminName := strings.TrimSpace(input.AdminFirstName + " " + input.AdminLastName)
	activationCode, err := generateActivationCode(input.Subdomain)
	if err != nil {
		return err
	}
	_, err = h.hrms.Services.Hrms.ProvisionTenant(ctx, hrmsports.ProvisionTenantCommand{
		TenantID:             tenantID,
		CompanyName:          input.CompanyName,
		Subdomain:            input.Subdomain,
		MobileActivationCode: activationCode,
		AdminEmail:           &input.AdminEmail,
		AdminName:            &adminName,
		TenantURL:            &tenantURL,
	})
	return err
}

func (h *Handler) ensureSignupTenantSubscription(ctx context.Context, tenantID uuid.UUID, planID uuid.UUID, planEmployeeLimit int32, trialEndsAt time.Time) (uuid.UUID, error) {
	var subscriptionID uuid.UUID
	err := h.db.QueryRow(ctx, `
		SELECT id
		FROM hrms.tenant_subscriptions
		WHERE tenant_id = $1
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&subscriptionID)
	if err == nil {
		return subscriptionID, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, fmt.Errorf("check existing signup tenant subscription: %w", err)
	}

	status := hrmsdomain.SubscriptionStatusTrialing
	endDate := trialEndsAt.Format("2006-01-02")
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		subscription, err := h.hrms.Services.Hrms.CreateTenantSubscription(systemCtx, hrmsports.TenantSubscriptionCommand{
			TenantID:     tenantID,
			PlanID:       &planID,
			StartDate:    time.Now().UTC().Format("2006-01-02"),
			EndDate:      endDate,
			Status:       status,
			MaxEmployees: planEmployeeLimit,
		})
		if err != nil {
			return err
		}
		subscriptionID = subscription.ID
		return nil
	}); err != nil {
		return uuid.Nil, err
	}
	return subscriptionID, nil
}

func ensureAssistedIdentityUserUnique(ctx context.Context, email, mobile string) error {
	tx := identitydb.GetTx(ctx)
	if tx == nil {
		return errors.New("identity transaction is not available")
	}
	var emailExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.users WHERE lower(email) = lower($1))`, email).Scan(&emailExists); err != nil {
		return err
	}
	if emailExists {
		return fmt.Errorf("%w: admin email already exists", errAssistedProvisionConflict)
	}
	var mobileExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.users WHERE mobile = $1)`, mobile).Scan(&mobileExists); err != nil {
		return err
	}
	if mobileExists {
		return fmt.Errorf("%w: admin mobile already exists", errAssistedProvisionConflict)
	}
	return nil
}

func ensureAssistedTenantAdminRole(ctx context.Context, tenantID uuid.UUID) (uuid.UUID, error) {
	tx := identitydb.GetTx(ctx)
	if tx == nil {
		return uuid.Nil, errors.New("identity transaction is not available")
	}
	var roleID uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO auth.roles (id, tenant_id, name, code, description, is_system)
		VALUES (gen_random_uuid(), $1, 'Tenant Admin', 'TENANT_ADMIN', 'Full access to modules assigned to this tenant', true)
		ON CONFLICT (tenant_id, name) DO UPDATE
		SET code = EXCLUDED.code,
			description = EXCLUDED.description,
			is_system = TRUE
		RETURNING id
	`, tenantID).Scan(&roleID)
	return roleID, err
}

func grantAssistedTenantBaselineModules(ctx context.Context, tenantID uuid.UUID, trialEndsAt *time.Time) error {
	tx := identitydb.GetTx(ctx)
	if tx == nil {
		return errors.New("identity transaction is not available")
	}
	moduleCodes := []string{"identity", "profile", "dashboard", "hrms"}
	_, err := tx.Exec(ctx, `
		INSERT INTO auth.tenant_modules (
			tenant_id, module_id, status, access_source, starts_at, ends_at, enabled_at, updated_at, metadata
		)
		SELECT
			$1,
			m.id,
			'active',
			CASE WHEN lower(m.code) IN ('identity', 'profile', 'dashboard') THEN 'free_lifetime' ELSE 'trial' END,
			NOW(),
			CASE WHEN lower(m.code) IN ('identity', 'profile', 'dashboard') THEN NULL ELSE $3::timestamptz END,
			NOW(),
			NOW(),
			jsonb_build_object('assisted_provision', true)
		FROM auth.modules m
		WHERE lower(m.code) = ANY($2::text[])
		ON CONFLICT (tenant_id, module_id) DO UPDATE
		SET status = 'active',
			access_source = EXCLUDED.access_source,
			starts_at = EXCLUDED.starts_at,
			ends_at = EXCLUDED.ends_at,
			enabled_at = EXCLUDED.enabled_at,
			updated_at = NOW(),
			metadata = auth.tenant_modules.metadata || EXCLUDED.metadata
	`, tenantID, moduleCodes, trialEndsAt)
	return err
}

func grantAssistedTenantAdminPermissions(ctx context.Context, roleID uuid.UUID) error {
	tx := identitydb.GetTx(ctx)
	if tx == nil {
		return errors.New("identity transaction is not available")
	}
	moduleCodes := []string{"identity", "profile", "dashboard", "hrms"}
	_, err := tx.Exec(ctx, `
		INSERT INTO auth.role_permissions (role_id, permission_id)
		SELECT $1, p.id
		FROM auth.permissions p
		LEFT JOIN auth.modules m ON m.id = p.module_id
		WHERE lower(COALESCE(m.code, p.module)) = ANY($2::text[])
		ON CONFLICT DO NOTHING
	`, roleID, moduleCodes)
	return err
}

func (h *Handler) insertAssistedProvisionAuditEvent(ctx context.Context, actorID *uuid.UUID, tenantID, adminUserID uuid.UUID, input assistedProvisionInput, planCode string) error {
	if h == nil || h.db == nil {
		return nil
	}
	payload, err := json.Marshal(map[string]any{
		"tenant_id":             tenantID,
		"tenant_name":           input.CompanyName,
		"subdomain":             input.Subdomain,
		"admin_user_id":         adminUserID,
		"admin_email":           input.AdminEmail,
		"plan_code":             planCode,
		"trial_days":            input.TrialDays,
		"billing_mode":          input.BillingMode,
		"payment_method_status": input.PaymentMethodStatus,
	})
	if err != nil {
		return err
	}
	return identitydb.RunInTx(ctx, h.db, func(txCtx context.Context) error {
		tx := identitydb.GetTx(txCtx)
		if tx == nil {
			return errors.New("identity transaction is not available")
		}
		if _, err := tx.Exec(txCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			return err
		}
		_, err := tx.Exec(txCtx, `
			INSERT INTO auth.audit_events (
				id, actor_user_id, actor_tenant_id, target_tenant_id, action, entity, payload_json, request_id, created_at
			) VALUES (
				$1, $2, NULL, $3, 'tenant.assisted_provisioned', 'tenant', $4::jsonb, '', NOW()
			)
		`, uuid.New(), actorID, tenantID, string(payload))
		return err
	})
}

func passwordHashForAssistedProvision(input assistedProvisionInput) (string, string, error) {
	inviteStatus := "not_sent"
	if input.SendInvite {
		inviteStatus = "not_available"
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.TemporaryPassword), 14)
	if err != nil {
		return "", inviteStatus, err
	}
	return string(hash), inviteStatus, nil
}

func normalizeAssistedProvisionMobile(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, " ", "")
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "(", "")
	value = strings.ReplaceAll(value, ")", "")
	return value
}

func isReservedTenantLoginSubdomain(subdomain string) bool {
	_, ok := reservedTenantLoginSubdomains[strings.ToLower(strings.TrimSpace(subdomain))]
	return ok
}
