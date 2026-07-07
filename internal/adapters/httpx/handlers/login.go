package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	hrmsdomain "github.com/ranakdinesh/spur-hrms/core/domain"
)

type loginRequest struct {
	Identifier string `json:"identifier"`
	Email      string `json:"email"`
	Password   string `json:"password"`
}

type identityLoginResponse struct {
	AccessToken                  string `json:"access_token"`
	RefreshToken                 string `json:"refresh_token"`
	TokenType                    string `json:"token_type"`
	ExpiresIn                    int    `json:"expires_in"`
	LegacyPasswordMigrated       bool   `json:"legacy_password_migrated,omitempty"`
	PasswordMigrationRequired    bool   `json:"password_migration_required,omitempty"`
	PasswordMigrationMessage     string `json:"password_migration_message,omitempty"`
	LegacyPasswordMigrationError string `json:"legacy_password_migration_error,omitempty"`
	DefaultModule                string `json:"default_module,omitempty"`
}

func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Identifier = strings.TrimSpace(req.Identifier)
	req.Email = strings.TrimSpace(req.Email)
	if req.Identifier == "" {
		req.Identifier = req.Email
	}
	if req.Identifier == "" || req.Password == "" {
		writeError(w, http.StatusBadRequest, "identifier and password are required")
		return
	}

	expectedTenantID, tenantScoped, err := h.resolveTenantIDFromLoginHost(r)
	if err != nil {
		h.logSignupError(r.Context(), "resolve tenant login host", err)
		writeError(w, http.StatusNotFound, "tenant domain not found")
		return
	}

	resp, err := h.loginThroughIdentity(r, req)
	if err != nil {
		h.logSignupError(r.Context(), "setika login", err)
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if tenantScoped {
		if err := ensureAccessTokenTenant(resp.AccessToken, expectedTenantID); err != nil {
			h.logSignupError(r.Context(), "validate tenant login token", err)
			writeError(w, http.StatusUnauthorized, "invalid credentials for this tenant")
			return
		}
	}
	accessToken, expiresIn, err := h.extendAccessToken(resp.AccessToken)
	if err != nil {
		h.logSignupError(r.Context(), "extend setika access token", err)
		writeError(w, http.StatusInternalServerError, "failed to create login session")
		return
	}
	resp.AccessToken = accessToken
	resp.ExpiresIn = expiresIn
	if resp.TokenType == "" {
		resp.TokenType = "Bearer"
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) loginThroughIdentity(r *http.Request, req loginRequest) (*identityLoginResponse, error) {
	if h == nil || h.identity == nil {
		return nil, errors.New("login is not configured")
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()
	h.identity.RegisterRoutes(router)
	request := httptest.NewRequest(http.MethodPost, "/auth/login", bytes.NewReader(body)).WithContext(r.Context())
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
		return nil, fmt.Errorf("identity login failed with status %d", recorder.Code)
	}

	var result identityLoginResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		return nil, err
	}
	if result.AccessToken == "" {
		return nil, errors.New("identity login response is missing access token")
	}
	return &result, nil
}

func (h *Handler) resolveTenantIDFromLoginHost(r *http.Request) (uuid.UUID, bool, error) {
	if h == nil || h.hrms == nil {
		return uuid.Nil, false, nil
	}
	subdomain, ok := h.tenantSubdomainFromHost(requestHost(r))
	if !ok {
		return uuid.Nil, false, nil
	}
	var tenantID uuid.UUID
	err := h.hrms.Services.Hrms.RunAsSystem(r.Context(), func(systemCtx context.Context) error {
		profile, err := h.hrms.Services.Hrms.ResolveTenantBySubdomain(systemCtx, subdomain)
		if err != nil {
			return err
		}
		tenantID = profile.TenantID
		return nil
	})
	if err != nil {
		return uuid.Nil, true, err
	}
	return tenantID, true, nil
}

func (h *Handler) tenantSubdomainFromHost(host string) (string, bool) {
	baseDomain := "dev.setika.one"
	if h != nil && strings.TrimSpace(h.tenantBaseDomain) != "" {
		baseDomain = strings.Trim(strings.ToLower(h.tenantBaseDomain), ". ")
	}
	host = strings.ToLower(strings.Trim(host, ". "))
	if splitHost, _, err := net.SplitHostPort(host); err == nil {
		host = strings.Trim(splitHost, ". ")
	}
	if host == "" || host == baseDomain || host == "www."+baseDomain {
		return "", false
	}
	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) {
		return "", false
	}
	subdomain := strings.TrimSuffix(host, suffix)
	if strings.Contains(subdomain, ".") {
		return "", false
	}
	subdomain = hrmsdomain.NormalizeSubdomain(subdomain)
	if subdomain == "" {
		return "", false
	}
	if _, reserved := reservedTenantLoginSubdomains[subdomain]; reserved {
		return "", false
	}
	return subdomain, true
}

var reservedTenantLoginSubdomains = map[string]struct{}{
	"admin":    {},
	"api":      {},
	"app":      {},
	"auth":     {},
	"billing":  {},
	"dev":      {},
	"files":    {},
	"identity": {},
	"mail":     {},
	"staging":  {},
	"support":  {},
	"www":      {},
}

func requestHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	if host := strings.TrimSpace(r.Host); host != "" {
		return host
	}
	return strings.TrimSpace(r.Header.Get("X-Forwarded-Host"))
}

func ensureAccessTokenTenant(accessToken string, expectedTenantID uuid.UUID) error {
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(accessToken, claims)
	if err != nil {
		return fmt.Errorf("parse identity token: %w", err)
	}
	actualTenantID := firstNonEmptyString(claimsString(claims, "tid"), claimsString(claims, "tenant_id"))
	if actualTenantID == "" {
		return errors.New("identity token is missing tenant id")
	}
	if !strings.EqualFold(actualTenantID, expectedTenantID.String()) {
		return fmt.Errorf("identity token tenant %s does not match host tenant %s", actualTenantID, expectedTenantID)
	}
	return nil
}

func (h *Handler) extendAccessToken(accessToken string) (string, int, error) {
	if h == nil || h.privateKey == nil {
		return "", 0, errors.New("token signer is not configured")
	}
	claims := jwt.MapClaims{}
	_, _, err := jwt.NewParser().ParseUnverified(accessToken, claims)
	if err != nil {
		return "", 0, fmt.Errorf("parse identity token: %w", err)
	}

	now := time.Now()
	expiresIn := h.loginSessionTTL
	if expiresIn <= 0 {
		expiresIn = 180 * 24 * time.Hour
	}
	claims["iss"] = firstNonEmptyString(h.identityIssuer, claimsString(claims, "iss"))
	claims["iat"] = now.Unix()
	claims["exp"] = now.Add(expiresIn).Unix()
	claims["jti"] = uuid.New().String()

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	signed, err := token.SignedString(h.privateKey)
	if err != nil {
		return "", 0, err
	}
	return signed, int(expiresIn.Seconds()), nil
}

func claimsString(claims jwt.MapClaims, key string) string {
	value, ok := claims[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
