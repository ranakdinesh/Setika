package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	hrmsdomain "github.com/ranakdinesh/spur-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/spur-hrms/core/ports"
	identity "github.com/ranakdinesh/spur-identity"
)

var signupSubdomainCleanupPattern = regexp.MustCompile(`[^a-z0-9-]+`)

func (h *Handler) ensureSignupAccountAvailable(ctx context.Context, email, mobile string) error {
	var exists bool
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.users WHERE lower(email) = lower($1))`, email).Scan(&exists); err != nil {
		return fmt.Errorf("check signup email: %w", err)
	}
	if exists {
		return errors.New("email already exists")
	}
	if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.users WHERE mobile = $1)`, mobile).Scan(&exists); err != nil {
		return fmt.Errorf("check signup mobile: %w", err)
	}
	if exists {
		return errors.New("mobile already exists")
	}
	return nil
}

func (h *Handler) availableSignupSubdomain(ctx context.Context, companyName string) (string, error) {
	base := signupSubdomainBase(companyName)
	if base == "" {
		return "", errors.New("company name cannot produce a valid workspace address")
	}
	for index := 0; index < 50; index++ {
		candidate := base
		if index > 0 {
			candidate = fmt.Sprintf("%s-%d", base, index+1)
		}
		if len(candidate) > 30 {
			suffix := ""
			if index > 0 {
				suffix = fmt.Sprintf("-%d", index+1)
			}
			candidate = strings.Trim(candidate[:30-len(suffix)], "-") + suffix
		}
		if err := h.validateRequestedSignupSubdomain(ctx, candidate); err == nil {
			return candidate, nil
		}
	}
	return "", errors.New("could not find an available workspace address")
}

func (h *Handler) validateRequestedSignupSubdomain(ctx context.Context, subdomain string) error {
	subdomain = hrmsdomain.NormalizeSubdomain(subdomain)
	if subdomain == "" {
		return errors.New("workspace address is required")
	}
	if !assistedTenantSubdomainPattern.MatchString(subdomain) || len(subdomain) < 3 || len(subdomain) > 30 {
		return errors.New("workspace address is invalid")
	}
	if hrmsdomain.IsReservedTenantSubdomain(subdomain) || isReservedTenantLoginSubdomain(subdomain) {
		return errors.New("workspace address is reserved")
	}
	if hrmsdomain.HasTenantSubdomainBusinessSuffix(subdomain) || hrmsdomain.TenantSubdomainCollisionKey(subdomain) == "" {
		return errors.New("workspace address is not allowed")
	}
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		if _, err := h.hrms.Services.Hrms.ResolveTenantBySubdomain(systemCtx, subdomain); err == nil {
			return errors.New("workspace address is already taken")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return nil
	}); err != nil {
		return err
	}
	var pending bool
	if err := h.db.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM platform.signup_intents
			WHERE subdomain = $1
			  AND status IN ('pending_email_verification', 'email_verified')
			  AND expires_at > NOW()
		)
	`, subdomain).Scan(&pending); err != nil {
		return fmt.Errorf("check pending workspace address: %w", err)
	}
	if pending {
		return errors.New("workspace address is already reserved")
	}
	return nil
}

func signupSubdomainBase(companyName string) string {
	value := strings.ToLower(strings.TrimSpace(companyName))
	value = strings.ReplaceAll(value, "&", " and ")
	value = signupSubdomainCleanupPattern.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-")
	for strings.Contains(value, "--") {
		value = strings.ReplaceAll(value, "--", "-")
	}
	value = hrmsdomain.NormalizeSubdomain(value)
	if len(value) > 30 {
		value = strings.Trim(value[:30], "-")
	}
	return value
}

func (h *Handler) sendSignupVerificationEmail(ctx context.Context, intent signupIntentEmail) error {
	if h == nil || h.communication == nil {
		return errors.New("email delivery is not configured")
	}
	verificationURL := h.signupVerificationURL(intent.Token)
	return h.communication.SendEmailVerification(ctx, identity.EmailVerificationMessage{
		TenantID:        uuid.Nil,
		UserID:          uuid.Nil,
		Recipient:       intent.Email,
		FirstName:       intent.FirstName,
		VerificationURL: verificationURL,
		TemplateKey:     "setika.signup_intent.email_verification",
	})
}

func (h *Handler) signupVerificationURL(token string) string {
	base := strings.TrimRight(strings.TrimSpace(h.frontendURL), "/")
	if base == "" {
		base = "http://localhost:3000"
	}
	return base + "/verify-email?signup_token=" + url.QueryEscape(token)
}

func (h *Handler) loadPendingSignupIntent(ctx context.Context, tokenHash string) (*signupIntent, error) {
	var intent signupIntent
	err := h.db.QueryRow(ctx, `
		UPDATE platform.signup_intents
		SET status = 'email_verified',
			email_verified_at = NOW(),
			updated_at = NOW()
		WHERE verification_token_hash = $1
		  AND status = 'pending_email_verification'
		  AND expires_at > NOW()
		RETURNING id, first_name, last_name, email, mobile, password_hash, company_name, subdomain, country, timezone, trial_days
	`, tokenHash).Scan(
		&intent.ID,
		&intent.FirstName,
		&intent.LastName,
		&intent.Email,
		&intent.Mobile,
		&intent.PasswordHash,
		&intent.CompanyName,
		&intent.Subdomain,
		&intent.Country,
		&intent.Timezone,
		&intent.TrialDays,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errors.New("invalid or expired verification token")
		}
		return nil, fmt.Errorf("load signup intent: %w", err)
	}
	return &intent, nil
}

func (h *Handler) defaultPublicTrialPlan(ctx context.Context) (uuid.UUID, string, error) {
	var selectedID uuid.UUID
	var selectedCode string
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		plans, err := h.hrms.Services.Hrms.ListSubscriptionPlans(systemCtx)
		if err != nil {
			return err
		}
		for _, plan := range plans {
			if plan == nil || !plan.IsActive || plan.Inactive || strings.ToLower(strings.TrimSpace(plan.Visibility)) != hrmsdomain.SubscriptionPlanVisibilityPublic {
				continue
			}
			if strings.EqualFold(plan.Code, "STARTER") {
				selectedID = plan.ID
				selectedCode = plan.Code
				return nil
			}
			if selectedID == uuid.Nil {
				selectedID = plan.ID
				selectedCode = plan.Code
			}
		}
		return nil
	}); err != nil {
		return uuid.Nil, "", err
	}
	if selectedID == uuid.Nil {
		return uuid.Nil, "", errors.New("no active public trial plan is available")
	}
	return selectedID, selectedCode, nil
}

func (h *Handler) provisionVerifiedSignupTenant(ctx context.Context, input assistedProvisionInput, passwordHash string, identityVerificationTokenHash string) (*adminProvisionTenantResponse, error) {
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

	trialEndsAt := time.Now().UTC().AddDate(0, 0, int(input.TrialDays))
	var trialEndsAtPtr *time.Time
	if input.TrialDays > 0 {
		trialEndsAtPtr = &trialEndsAt
	}
	tenantID, adminUserID, err := h.createAssistedIdentityTenant(ctx, input, assistedIdentityTenantInput{
		PlanCode:                   planCode,
		TrialEndsAt:                trialEndsAtPtr,
		PasswordHash:               passwordHash,
		EmailVerificationTokenHash: identityVerificationTokenHash,
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
	}); err != nil {
		return nil, err
	}

	maxEmployees := planEmployeeLimit
	status := hrmsdomain.SubscriptionStatusTrialing
	endDate := trialEndsAt.Format("2006-01-02")
	var subscriptionID uuid.UUID
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		subscription, err := h.hrms.Services.Hrms.CreateTenantSubscription(systemCtx, hrmsports.TenantSubscriptionCommand{
			TenantID:     tenantID,
			PlanID:       &input.PlanID,
			StartDate:    time.Now().UTC().Format("2006-01-02"),
			EndDate:      endDate,
			Status:       status,
			MaxEmployees: maxEmployees,
		})
		if err != nil {
			return err
		}
		subscriptionID = subscription.ID
		return nil
	}); err != nil {
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

func (h *Handler) verifyIdentityEmailTokenThroughRoute(ctx context.Context, token string) (*identityLoginResponse, error) {
	if h == nil || h.identity == nil {
		return nil, errors.New("identity verification is not configured")
	}
	router := chi.NewRouter()
	h.identity.RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodGet, "/auth/email/verify?token="+url.QueryEscape(token), bytes.NewReader(nil)).WithContext(ctx)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code < 200 || recorder.Code >= 300 {
		var errBody map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &errBody); err == nil {
			if message, ok := errBody["error"].(string); ok && message != "" {
				return nil, errors.New(message)
			}
		}
		return nil, fmt.Errorf("identity email verification failed with status %d", recorder.Code)
	}
	var result identityLoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		return nil, err
	}
	if result.AccessToken == "" || result.RefreshToken == "" {
		return nil, errors.New("identity email verification response is missing tokens")
	}
	return &result, nil
}

func generateSignupToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func hashSignupToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
