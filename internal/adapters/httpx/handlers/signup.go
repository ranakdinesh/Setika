package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	identityadapter "github.com/ranakdinesh/setika/internal/adapters/identity"
	"github.com/ranakdinesh/setika/internal/logger"
	hrms "github.com/ranakdinesh/spur-hrms"
	hrmsdomain "github.com/ranakdinesh/spur-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/spur-hrms/core/ports"
	identity "github.com/ranakdinesh/spur-identity"
	goredis "github.com/redis/go-redis/v9"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	identity         *identity.Module
	hrms             *hrms.Module
	communication    identity.CommunicationPort
	db               *pgxpool.Pool
	redis            *goredis.Client
	log              *logger.Loggerx
	privateKey       *rsa.PrivateKey
	identityIssuer   string
	loginSessionTTL  time.Duration
	tenantBaseDomain string
	frontendURL      string
	signupAlertEmail string
}

type Options struct {
	Identity         *identity.Module
	Hrms             *hrms.Module
	Communication    identity.CommunicationPort
	DB               *pgxpool.Pool
	Redis            *goredis.Client
	Log              *logger.Loggerx
	PrivateKey       *rsa.PrivateKey
	IdentityIssuer   string
	LoginSessionTTL  time.Duration
	TenantBaseDomain string
	FrontendURL      string
	SignupAlertEmail string
}

func New(opts Options) *Handler {
	loginSessionTTL := opts.LoginSessionTTL
	if loginSessionTTL <= 0 {
		loginSessionTTL = 180 * 24 * time.Hour
	}
	tenantBaseDomain := strings.Trim(opts.TenantBaseDomain, ". ")
	if tenantBaseDomain == "" {
		tenantBaseDomain = "dev.setika.one"
	}
	return &Handler{
		identity:         opts.Identity,
		hrms:             opts.Hrms,
		communication:    opts.Communication,
		db:               opts.DB,
		redis:            opts.Redis,
		log:              opts.Log,
		privateKey:       opts.PrivateKey,
		identityIssuer:   opts.IdentityIssuer,
		loginSessionTTL:  loginSessionTTL,
		tenantBaseDomain: tenantBaseDomain,
		frontendURL:      strings.TrimRight(strings.TrimSpace(opts.FrontendURL), "/"),
		signupAlertEmail: strings.TrimSpace(opts.SignupAlertEmail),
	}
}

type signupRequest struct {
	FirstName   string `json:"firstName"`
	LastName    string `json:"lastName"`
	Email       string `json:"email"`
	Mobile      string `json:"phone"`
	CompanyName string `json:"company"`
	Employees   string `json:"employees"`
	Subdomain   string `json:"subdomain"`
	Password    string `json:"password"`
}

type signupResponse struct {
	TenantID             string `json:"tenant_id"`
	UserID               string `json:"user_id"`
	Subdomain            string `json:"subdomain"`
	TenantURL            string `json:"tenant_url"`
	MobileActivationCode string `json:"mobile_activation_code"`
	Message              string `json:"message"`
	RequiresVerification bool   `json:"requires_email_verification"`
	Status               string `json:"status,omitempty"`
}

type signupSubdomainAvailabilityResponse struct {
	Subdomain string `json:"subdomain"`
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

type signupVerifyResponse struct {
	AccessToken          string `json:"access_token"`
	RefreshToken         string `json:"refresh_token"`
	TokenType            string `json:"token_type"`
	ExpiresIn            int    `json:"expires_in"`
	DefaultModule        string `json:"default_module,omitempty"`
	TenantID             string `json:"tenant_id"`
	UserID               string `json:"user_id"`
	Subdomain            string `json:"subdomain"`
	TenantURL            string `json:"tenant_url"`
	PlanCode             string `json:"plan_code"`
	Message              string `json:"message"`
	Status               string `json:"status"`
	RequiresVerification bool   `json:"requires_email_verification"`
}

type identityRegisterResponse struct {
	Tenant struct {
		ID string `json:"id"`
	} `json:"Tenant"`
	User struct {
		ID string `json:"id"`
	} `json:"User"`
}

func (h *Handler) Signup(w http.ResponseWriter, r *http.Request) {
	var req signupRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logSignupError(r.Context(), "decode signup request", err)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	resp, err := h.createSignupIntent(r.Context(), req, clientIPAddress(r))
	if err != nil {
		h.logSignupError(r.Context(), "create signup intent", err)
		status := http.StatusBadRequest
		if errors.Is(err, errSignupRateLimited) {
			status = http.StatusTooManyRequests
		}
		writeError(w, status, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) VerifySignup(w http.ResponseWriter, r *http.Request) {
	token := strings.TrimSpace(r.URL.Query().Get("token"))
	if token == "" {
		writeError(w, http.StatusBadRequest, "verification token is required")
		return
	}
	resp, err := h.verifySignupIntent(r.Context(), token)
	if err != nil {
		h.logSignupError(r.Context(), "verify signup intent", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) SignupSubdomainAvailability(w http.ResponseWriter, r *http.Request) {
	subdomain := hrmsdomain.NormalizeSubdomain(r.URL.Query().Get("subdomain"))
	resp := signupSubdomainAvailabilityResponse{
		Subdomain: subdomain,
		Available: false,
	}
	if subdomain == "" {
		resp.Message = "workspace address is required"
	} else if err := h.validateRequestedSignupSubdomain(r.Context(), subdomain); err != nil {
		resp.Message = err.Error()
	} else {
		resp.Available = true
		resp.Message = "workspace address is available"
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) createSignupIntent(ctx context.Context, req signupRequest, clientIP string) (*signupResponse, error) {
	if h == nil || h.identity == nil || h.hrms == nil || h.db == nil {
		err := errors.New("signup is not configured")
		h.logSignupError(ctx, "validate signup dependencies", err)
		return nil, err
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Mobile = normalizeAssistedProvisionMobile(req.Mobile)
	req.CompanyName = strings.TrimSpace(req.CompanyName)
	req.Subdomain = hrmsdomain.NormalizeSubdomain(req.Subdomain)
	if req.FirstName == "" || req.LastName == "" || req.Email == "" || req.Mobile == "" || req.CompanyName == "" || req.Password == "" || req.Subdomain == "" {
		err := errors.New("first name, last name, email, mobile, company, workspace address, and password are required")
		h.logSignupError(ctx, "validate signup request", err)
		return nil, err
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return nil, errors.New("email is invalid")
	}
	if len(req.Password) < assistedProvisionTempPasswordMin {
		return nil, fmt.Errorf("password must be at least %d characters", assistedProvisionTempPasswordMin)
	}
	if err := h.enforceSignupRateLimits(ctx, signupRateLimitSubject{
		IP:        clientIP,
		Email:     req.Email,
		Mobile:    req.Mobile,
		Subdomain: req.Subdomain,
	}); err != nil {
		return nil, err
	}
	if err := h.ensureSignupAccountAvailable(ctx, req.Email, req.Mobile); err != nil {
		return nil, err
	}
	if err := h.validateRequestedSignupSubdomain(ctx, req.Subdomain); err != nil {
		return nil, err
	}

	passwordHash, err := bcrypt.GenerateFromPassword([]byte(req.Password), 14)
	if err != nil {
		return nil, err
	}
	token, err := generateSignupToken()
	if err != nil {
		return nil, err
	}
	tokenHash := hashSignupToken(token)
	expiresAt := signupTokenExpiry()
	intentID := uuid.New()
	if _, err := h.db.Exec(ctx, `
		UPDATE platform.signup_intents
		SET status = 'cancelled', updated_at = NOW()
		WHERE status = 'pending_email_verification'
		  AND (lower(email) = lower($1) OR mobile = $2)
	`, req.Email, req.Mobile); err != nil {
		return nil, fmt.Errorf("cancel previous signup intent: %w", err)
	}
	if _, err := h.db.Exec(ctx, `
		INSERT INTO platform.signup_intents (
			id, first_name, last_name, email, mobile, password_hash, company_name, subdomain,
			country, timezone, trial_days, status, verification_token_hash, verification_sent_at, expires_at
		) VALUES (
			$1, $2, $3, $4, $5, $6, $7, $8,
			'IN', 'Asia/Kolkata', $9, 'pending_email_verification', $10, NOW(), $11
		)
	`, intentID, req.FirstName, req.LastName, req.Email, req.Mobile, string(passwordHash), req.CompanyName, req.Subdomain, defaultAssistedProvisionTrialDays, tokenHash, expiresAt); err != nil {
		return nil, fmt.Errorf("create signup intent: %w", err)
	}
	emailIntent := signupIntentEmail{
		ID: intentID, FirstName: req.FirstName, LastName: req.LastName, Email: req.Email, Token: token,
	}
	if err := h.sendSignupVerificationEmail(ctx, emailIntent); err != nil {
		return nil, err
	}
	h.notifySuperAdminsOfSignupIntent(ctx, emailIntent, req.CompanyName, req.Mobile, req.Subdomain)

	return &signupResponse{
		Subdomain:            req.Subdomain,
		TenantURL:            "https://" + h.tenantHost(req.Subdomain),
		Message:              "Check your email to verify your account. Your 30-day Setika trial starts after verification.",
		RequiresVerification: true,
		Status:               "pending_email_verification",
	}, nil
}

type signupIntent struct {
	ID           uuid.UUID
	FirstName    string
	LastName     string
	Email        string
	Mobile       string
	PasswordHash string
	CompanyName  string
	Subdomain    string
	Country      string
	Timezone     string
	TrialDays    int32
}

type signupIntentEmail struct {
	ID        uuid.UUID
	FirstName string
	LastName  string
	Email     string
	Token     string
}

func (h *Handler) verifySignupIntent(ctx context.Context, token string) (*signupVerifyResponse, error) {
	if h == nil || h.db == nil || h.hrms == nil || h.identity == nil {
		return nil, errors.New("signup verification is not configured")
	}
	intent, err := h.loadPendingSignupIntent(ctx, hashSignupToken(token))
	if err != nil {
		return nil, err
	}
	planID, planCode, err := h.defaultPublicTrialPlan(ctx)
	if err != nil {
		return nil, err
	}
	identityVerificationToken, err := generateSignupToken()
	if err != nil {
		return nil, err
	}
	input := assistedProvisionInput{
		CompanyName:         intent.CompanyName,
		LegalName:           intent.CompanyName,
		Subdomain:           intent.Subdomain,
		EmployeeEstimate:    0,
		Country:             intent.Country,
		Timezone:            intent.Timezone,
		AdminFirstName:      intent.FirstName,
		AdminLastName:       intent.LastName,
		AdminEmail:          intent.Email,
		AdminMobile:         intent.Mobile,
		PlanID:              planID,
		TrialDays:           intent.TrialDays,
		BillingMode:         "manual_billing",
		PaymentMethodStatus: "manual_billing",
		SendInvite:          false,
	}
	result, err := h.provisionVerifiedSignupTenant(ctx, input, intent.PasswordHash, hashSignupToken(identityVerificationToken))
	if err != nil {
		return nil, err
	}
	if _, err := h.db.Exec(ctx, `
		UPDATE platform.signup_intents
		SET status = 'provisioned',
			email_verified_at = COALESCE(email_verified_at, NOW()),
			provisioned_tenant_id = $2,
			provisioned_user_id = $3,
			provisioned_subscription_id = $4,
			updated_at = NOW()
		WHERE id = $1
	`, intent.ID, result.TenantID, result.AdminUserID, result.SubscriptionID); err != nil {
		return nil, fmt.Errorf("mark signup intent provisioned: %w", err)
	}
	h.sendSignupWelcomeEmail(ctx, intent, result)
	login, err := h.verifyIdentityEmailTokenThroughRoute(ctx, identityVerificationToken)
	if err != nil {
		return nil, err
	}
	accessToken, expiresIn, err := h.extendAccessToken(login.AccessToken)
	if err != nil {
		return nil, err
	}

	return &signupVerifyResponse{
		AccessToken:          accessToken,
		RefreshToken:         login.RefreshToken,
		TokenType:            firstNonEmptyString(login.TokenType, "Bearer"),
		ExpiresIn:            expiresIn,
		DefaultModule:        firstNonEmptyString(login.DefaultModule, "/dashboard"),
		TenantID:             result.TenantID.String(),
		UserID:               result.AdminUserID.String(),
		Subdomain:            result.Subdomain,
		TenantURL:            result.TenantURL,
		PlanCode:             planCode,
		Message:              "Email verified. Your Setika trial workspace is ready.",
		Status:               "verified",
		RequiresVerification: false,
	}, nil
}

func (h *Handler) assignBaselineEmployeeRole(ctx context.Context, tenantID uuid.UUID, userID uuid.UUID) error {
	adapter, err := identityadapter.NewEmployeeIdentityAdapter(h.identity)
	if err != nil {
		return err
	}
	return adapter.AssignEmployeeRole(ctx, hrmsports.AssignEmployeeRoleCommand{
		TenantID: tenantID,
		UserID:   userID,
		Role:     hrmsdomain.RoleEmployee,
	})
}

func (h *Handler) tenantHost(subdomain string) string {
	baseDomain := "dev.setika.one"
	if h != nil && h.tenantBaseDomain != "" {
		baseDomain = h.tenantBaseDomain
	}
	return subdomain + "." + strings.Trim(baseDomain, ". ")
}

func (h *Handler) registerIdentityTenant(ctx context.Context, req signupRequest) (*identityRegisterResponse, error) {
	payload := map[string]any{
		"first_name":   req.FirstName,
		"last_name":    req.LastName,
		"company_name": req.CompanyName,
		"email":        req.Email,
		"mobile":       req.Mobile,
		"password":     req.Password,
		"auto_verify":  false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()
	h.identity.RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodPost, "/auth/register", bytes.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code < 200 || recorder.Code >= 300 {
		var errBody map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &errBody); err == nil {
			if message, ok := errBody["error"].(string); ok && message != "" {
				return nil, errors.New(message)
			}
		}
		return nil, fmt.Errorf("identity registration failed with status %d", recorder.Code)
	}

	var result identityRegisterResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		return nil, err
	}
	if result.Tenant.ID == "" || result.User.ID == "" {
		return nil, errors.New("identity registration response is missing tenant or user")
	}
	return &result, nil
}

func (h *Handler) logSignupError(ctx context.Context, operation string, err error) {
	if h == nil || h.log == nil || err == nil {
		return
	}
	h.log.Error(ctx).Err(err).Str("operation", operation).Msg("signup failed")
}

func generateActivationCode(subdomain string) (string, error) {
	b := make([]byte, 3)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	prefix := strings.ToUpper(strings.ReplaceAll(subdomain, "-", ""))
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	if prefix == "" {
		prefix = "SETIKA"
	}
	return prefix + "-" + strings.ToUpper(hex.EncodeToString(b)), nil
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
