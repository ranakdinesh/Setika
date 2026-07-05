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
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	hrms "github.com/ranakdinesh/setika-hrms"
	hrmsdomain "github.com/ranakdinesh/setika-hrms/core/domain"
	hrmsports "github.com/ranakdinesh/setika-hrms/core/ports"
	identityadapter "github.com/ranakdinesh/setika/internal/adapters/identity"
	"github.com/ranakdinesh/setika/internal/logger"
	identity "github.com/ranakdinesh/spur-identity"
)

type Handler struct {
	identity         *identity.Module
	hrms             *hrms.Module
	db               *pgxpool.Pool
	log              *logger.Loggerx
	privateKey       *rsa.PrivateKey
	identityIssuer   string
	loginSessionTTL  time.Duration
	tenantBaseDomain string
}

type Options struct {
	Identity         *identity.Module
	Hrms             *hrms.Module
	DB               *pgxpool.Pool
	Log              *logger.Loggerx
	PrivateKey       *rsa.PrivateKey
	IdentityIssuer   string
	LoginSessionTTL  time.Duration
	TenantBaseDomain string
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
		db:               opts.DB,
		log:              opts.Log,
		privateKey:       opts.PrivateKey,
		identityIssuer:   opts.IdentityIssuer,
		loginSessionTTL:  loginSessionTTL,
		tenantBaseDomain: tenantBaseDomain,
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
	resp, err := h.registerTenantSignup(r.Context(), req)
	if err != nil {
		h.logSignupError(r.Context(), "register tenant signup", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) registerTenantSignup(ctx context.Context, req signupRequest) (*signupResponse, error) {
	if h == nil || h.identity == nil || h.hrms == nil {
		err := errors.New("signup is not configured")
		h.logSignupError(ctx, "validate signup dependencies", err)
		return nil, err
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Mobile = strings.TrimSpace(req.Mobile)
	req.CompanyName = strings.TrimSpace(req.CompanyName)
	req.Subdomain = hrmsdomain.NormalizeSubdomain(req.Subdomain)
	if req.FirstName == "" || req.LastName == "" || req.Email == "" || req.Mobile == "" || req.CompanyName == "" || req.Subdomain == "" || req.Password == "" {
		err := errors.New("first name, last name, email, mobile, company, subdomain, and password are required")
		h.logSignupError(ctx, "validate signup request", err)
		return nil, err
	}
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		if _, err := h.hrms.Services.Hrms.ResolveTenantBySubdomain(systemCtx, req.Subdomain); err == nil {
			return errors.New("subdomain is already taken")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("check subdomain availability: %w", err)
		}
		return nil
	}); err != nil {
		h.logSignupError(ctx, "check signup availability", err)
		return nil, err
	}

	registered, err := h.registerIdentityTenant(ctx, req)
	if err != nil {
		err = fmt.Errorf("create identity tenant: %w", err)
		h.logSignupError(ctx, "create identity tenant", err)
		return nil, err
	}
	tenantID, err := uuid.Parse(registered.Tenant.ID)
	if err != nil {
		err = fmt.Errorf("identity returned invalid tenant id: %w", err)
		h.logSignupError(ctx, "parse identity tenant id", err)
		return nil, err
	}
	userID, err := uuid.Parse(registered.User.ID)
	if err != nil {
		err = fmt.Errorf("identity returned invalid user id: %w", err)
		h.logSignupError(ctx, "parse identity user id", err)
		return nil, err
	}
	if err := h.assignBaselineEmployeeRole(ctx, tenantID, userID); err != nil {
		err = fmt.Errorf("assign baseline employee role: %w", err)
		h.logSignupError(ctx, "assign baseline employee role", err)
		return nil, err
	}

	code, err := generateActivationCode(req.Subdomain)
	if err != nil {
		h.logSignupError(ctx, "generate activation code", err)
		return nil, err
	}
	displayName := req.CompanyName
	var profileSubdomain string
	var activationCode string
	if err := h.hrms.Services.Hrms.RunAsSystem(ctx, func(systemCtx context.Context) error {
		profile, err := h.hrms.Services.Hrms.UpsertTenantProfile(systemCtx, hrmsports.UpsertTenantProfileCmd{
			TenantID:             tenantID,
			Subdomain:            req.Subdomain,
			MobileActivationCode: code,
			DisplayName:          &displayName,
		})
		if err != nil {
			return fmt.Errorf("create hrms tenant profile: %w", err)
		}
		profileSubdomain = profile.Subdomain
		activationCode = profile.MobileActivationCode

		if _, err := h.hrms.Services.Hrms.UpsertTenantSetting(systemCtx, hrmsports.UpsertTenantSettingCmd{
			TenantID: tenantID,
			Key:      "theme",
			Value: map[string]any{
				"primaryColor": "#588368",
				"mode":         "light",
			},
		}); err != nil {
			return fmt.Errorf("create default theme: %w", err)
		}
		if _, err := h.hrms.Services.Hrms.UpsertTenantSetting(systemCtx, hrmsports.UpsertTenantSettingCmd{
			TenantID: tenantID,
			Key:      "company",
			Value: map[string]any{
				"name":      req.CompanyName,
				"employees": req.Employees,
			},
		}); err != nil {
			return fmt.Errorf("create company settings: %w", err)
		}

		return nil
	}); err != nil {
		h.logSignupError(ctx, "provision hrms tenant", err)
		return nil, err
	}

	tenantURL := h.tenantHost(profileSubdomain)
	return &signupResponse{
		TenantID:             registered.Tenant.ID,
		UserID:               userID.String(),
		Subdomain:            profileSubdomain,
		TenantURL:            "https://" + tenantURL,
		MobileActivationCode: activationCode,
		Message:              "Signup created successfully. Please verify your email address before logging in.",
		RequiresVerification: true,
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
