package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type adminSignupIntentResponse struct {
	ID                        string     `json:"id"`
	FirstName                 string     `json:"first_name"`
	LastName                  string     `json:"last_name"`
	Email                     string     `json:"email"`
	Mobile                    string     `json:"mobile"`
	CompanyName               string     `json:"company_name"`
	Subdomain                 string     `json:"subdomain"`
	TenantURL                 string     `json:"tenant_url"`
	Country                   string     `json:"country"`
	Timezone                  string     `json:"timezone"`
	TrialDays                 int32      `json:"trial_days"`
	Status                    string     `json:"status"`
	EmailTokenExpired         bool       `json:"email_token_expired"`
	VerificationSentAt        time.Time  `json:"verification_sent_at"`
	EmailVerifiedAt           *time.Time `json:"email_verified_at,omitempty"`
	ExpiresAt                 time.Time  `json:"expires_at"`
	ProvisionedTenantID       *string    `json:"provisioned_tenant_id,omitempty"`
	ProvisionedUserID         *string    `json:"provisioned_user_id,omitempty"`
	ProvisionedSubscriptionID *string    `json:"provisioned_subscription_id,omitempty"`
	CreatedAt                 time.Time  `json:"created_at"`
	UpdatedAt                 time.Time  `json:"updated_at"`
}

type adminUpdateSignupIntentRequest struct {
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	Email       string `json:"email"`
	Mobile      string `json:"mobile"`
	CompanyName string `json:"company_name"`
	Subdomain   string `json:"subdomain"`
	Country     string `json:"country"`
	Timezone    string `json:"timezone"`
	TrialDays   int32  `json:"trial_days"`
}

func (h *Handler) AdminListSignupIntents(w http.ResponseWriter, r *http.Request) {
	if !hasAssistedTenantProvisionAccess(r.Context()) {
		writeError(w, http.StatusForbidden, "platform signup request access required")
		return
	}
	if h == nil || h.db == nil {
		writeError(w, http.StatusInternalServerError, "signup request handler is not configured")
		return
	}

	status := strings.TrimSpace(r.URL.Query().Get("status"))
	rows, err := h.db.Query(r.Context(), `
		SELECT
			id::text,
			first_name,
			last_name,
			email,
			mobile,
			company_name,
			subdomain,
			country,
			timezone,
			trial_days,
			status,
			verification_sent_at,
			email_verified_at,
			expires_at,
			provisioned_tenant_id::text,
			provisioned_user_id::text,
			provisioned_subscription_id::text,
			created_at,
			updated_at
		FROM platform.signup_intents
		WHERE (
			($1 = '' AND status IN ('pending_email_verification', 'email_verified') AND provisioned_tenant_id IS NULL)
			OR $1 = 'all'
			OR ($1 <> '' AND $1 <> 'all' AND status = $1)
		)
		ORDER BY created_at DESC
		LIMIT 200
	`, status)
	if err != nil {
		h.logSignupError(r.Context(), "list signup intents", err)
		writeError(w, http.StatusInternalServerError, "unable to load signup requests")
		return
	}
	defer rows.Close()

	requests := make([]adminSignupIntentResponse, 0)
	for rows.Next() {
		var item adminSignupIntentResponse
		var emailVerifiedAt sql.NullTime
		var provisionedTenantID sql.NullString
		var provisionedUserID sql.NullString
		var provisionedSubscriptionID sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.FirstName,
			&item.LastName,
			&item.Email,
			&item.Mobile,
			&item.CompanyName,
			&item.Subdomain,
			&item.Country,
			&item.Timezone,
			&item.TrialDays,
			&item.Status,
			&item.VerificationSentAt,
			&emailVerifiedAt,
			&item.ExpiresAt,
			&provisionedTenantID,
			&provisionedUserID,
			&provisionedSubscriptionID,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			h.logSignupError(r.Context(), "scan signup intent", err)
			writeError(w, http.StatusInternalServerError, "unable to load signup requests")
			return
		}
		item.TenantURL = "https://" + h.tenantHost(item.Subdomain)
		item.EmailTokenExpired = item.Status == "pending_email_verification" && time.Now().UTC().After(item.ExpiresAt)
		item.EmailVerifiedAt = nullTimePtr(emailVerifiedAt)
		item.ProvisionedTenantID = nullStringPtr(provisionedTenantID)
		item.ProvisionedUserID = nullStringPtr(provisionedUserID)
		item.ProvisionedSubscriptionID = nullStringPtr(provisionedSubscriptionID)
		requests = append(requests, item)
	}
	if err := rows.Err(); err != nil {
		h.logSignupError(r.Context(), "iterate signup intents", err)
		writeError(w, http.StatusInternalServerError, "unable to load signup requests")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(requests)
}

func (h *Handler) AdminUpdateSignupIntent(w http.ResponseWriter, r *http.Request) {
	if !hasAssistedTenantProvisionAccess(r.Context()) {
		writeError(w, http.StatusForbidden, "platform signup request access required")
		return
	}
	if h == nil || h.db == nil || h.hrms == nil {
		writeError(w, http.StatusInternalServerError, "signup request handler is not configured")
		return
	}

	intentID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "intentID")))
	if err != nil || intentID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "signup request id is invalid")
		return
	}
	var req adminUpdateSignupIntentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logSignupError(r.Context(), "decode signup request update", err)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	current, err := h.loadSignupIntentForAdminEdit(r.Context(), intentID)
	if err != nil {
		h.logSignupError(r.Context(), "load signup request for edit", err)
		status := http.StatusInternalServerError
		if errors.Is(err, pgx.ErrNoRows) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	if current.Status == "provisioned" || current.ProvisionedTenantID.Valid {
		writeError(w, http.StatusBadRequest, "provisioned signup requests cannot be edited")
		return
	}

	req.normalize()
	if err := req.validate(current); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Email != current.Email && current.EmailVerifiedAt.Valid {
		writeError(w, http.StatusBadRequest, "verified email cannot be changed")
		return
	}
	if err := h.ensureSignupIntentUpdateUnique(r.Context(), intentID, current, req); err != nil {
		h.logSignupError(r.Context(), "validate signup request update uniqueness", err)
		status := http.StatusBadRequest
		if strings.Contains(err.Error(), "already") {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}

	token := ""
	tokenHash := current.VerificationTokenHash
	expiresAt := current.ExpiresAt
	verificationSentAtSQL := "verification_sent_at"
	if req.Email != current.Email && !current.EmailVerifiedAt.Valid {
		token, err = generateSignupToken()
		if err != nil {
			h.logSignupError(r.Context(), "generate corrected signup verification token", err)
			writeError(w, http.StatusInternalServerError, "unable to prepare corrected verification email")
			return
		}
		tokenHash = hashSignupToken(token)
		expiresAt = signupTokenExpiry()
		verificationSentAtSQL = "NOW()"
	}

	query := fmt.Sprintf(`
		UPDATE platform.signup_intents
		SET first_name = $2,
			last_name = $3,
			email = $4,
			mobile = $5,
			company_name = $6,
			subdomain = $7,
			country = $8,
			timezone = $9,
			trial_days = $10,
			status = CASE WHEN $11::bool THEN 'pending_email_verification' ELSE status END,
			email_verified_at = CASE WHEN $11::bool THEN NULL ELSE email_verified_at END,
			verification_token_hash = $12,
			verification_sent_at = %s,
			expires_at = $13,
			updated_at = NOW()
		WHERE id = $1
		  AND status IN ('pending_email_verification', 'email_verified')
		  AND provisioned_tenant_id IS NULL
	`, verificationSentAtSQL)
	emailChanged := req.Email != current.Email && !current.EmailVerifiedAt.Valid
	if _, err := h.db.Exec(r.Context(), query,
		intentID,
		req.FirstName,
		req.LastName,
		req.Email,
		req.Mobile,
		req.CompanyName,
		req.Subdomain,
		req.Country,
		req.Timezone,
		req.TrialDays,
		emailChanged,
		tokenHash,
		expiresAt,
	); err != nil {
		h.logSignupError(r.Context(), "update signup request", err)
		writeError(w, http.StatusInternalServerError, "unable to update signup request")
		return
	}

	if emailChanged {
		if err := h.sendSignupVerificationEmail(r.Context(), signupIntentEmail{
			ID: intentID, FirstName: req.FirstName, LastName: req.LastName, Email: req.Email, Token: token,
		}); err != nil {
			h.logSignupError(r.Context(), "send corrected signup verification email", err)
			writeError(w, http.StatusInternalServerError, "signup request was updated but verification email could not be sent")
			return
		}
	}

	updated, err := h.loadSignupIntentResponse(r.Context(), intentID)
	if err != nil {
		h.logSignupError(r.Context(), "load updated signup request", err)
		writeError(w, http.StatusInternalServerError, "signup request was updated but could not be reloaded")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(updated)
}

func (h *Handler) AdminDeleteSignupIntent(w http.ResponseWriter, r *http.Request) {
	if !hasAssistedTenantProvisionAccess(r.Context()) {
		writeError(w, http.StatusForbidden, "platform signup request access required")
		return
	}
	if h == nil || h.db == nil {
		writeError(w, http.StatusInternalServerError, "signup request handler is not configured")
		return
	}

	intentID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "intentID")))
	if err != nil || intentID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "signup request id is invalid")
		return
	}
	tag, err := h.db.Exec(r.Context(), `
		DELETE FROM platform.signup_intents
		WHERE id = $1
		  AND status <> 'provisioned'
		  AND provisioned_tenant_id IS NULL
		  AND provisioned_user_id IS NULL
		  AND provisioned_subscription_id IS NULL
	`, intentID)
	if err != nil {
		h.logSignupError(r.Context(), "delete signup request", err)
		writeError(w, http.StatusInternalServerError, "unable to delete signup request")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusBadRequest, "only non-provisioned signup requests can be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) AdminManualProvisionSignupIntent(w http.ResponseWriter, r *http.Request) {
	if !hasAssistedTenantProvisionAccess(r.Context()) {
		writeError(w, http.StatusForbidden, "platform signup request access required")
		return
	}
	if h == nil || h.db == nil || h.hrms == nil || h.identity == nil {
		writeError(w, http.StatusInternalServerError, "signup request handler is not configured")
		return
	}

	intentID, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "intentID")))
	if err != nil || intentID == uuid.Nil {
		writeError(w, http.StatusBadRequest, "signup request id is invalid")
		return
	}
	intent, err := h.loadSignupIntentForManualProvision(r.Context(), intentID)
	if err != nil {
		h.logSignupError(r.Context(), "load call-verified signup intent", err)
		status := http.StatusInternalServerError
		if errors.Is(err, pgNoRowsLike) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	planID, _, err := h.defaultPublicTrialPlan(r.Context())
	if err != nil {
		h.logSignupError(r.Context(), "select default trial plan for manual signup provision", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
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
	result, err := h.provisionVerifiedSignupTenant(r.Context(), input, intent.PasswordHash, "")
	if err != nil {
		if errors.Is(err, errAssistedProvisionConflict) {
			if resumed, resumeErr := h.resumePartialSignupTenantProvision(r.Context(), input); resumeErr == nil {
				result = resumed
				err = nil
			} else if !errors.Is(resumeErr, pgx.ErrNoRows) {
				err = resumeErr
			}
		}
		if err != nil {
			h.logSignupError(r.Context(), "call-verified signup tenant provision", err)
			status := http.StatusInternalServerError
			if errors.Is(err, errAssistedProvisionConflict) {
				status = http.StatusConflict
			} else if errors.Is(err, errAssistedProvisionInvalid) {
				status = http.StatusBadRequest
			}
			writeError(w, status, err.Error())
			return
		}
	}
	if _, err := h.db.Exec(r.Context(), `
		UPDATE platform.signup_intents
		SET status = 'provisioned',
			email_verified_at = COALESCE(email_verified_at, NOW()),
			provisioned_tenant_id = $2,
			provisioned_user_id = $3,
			provisioned_subscription_id = $4,
			updated_at = NOW()
		WHERE id = $1
	`, intent.ID, result.TenantID, result.AdminUserID, result.SubscriptionID); err != nil {
		h.logSignupError(r.Context(), "mark manual signup intent provisioned", err)
		writeError(w, http.StatusInternalServerError, "tenant was created but signup request status could not be updated")
		return
	}
	h.sendSignupWelcomeEmail(r.Context(), intent, result)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(result)
}

var pgNoRowsLike = errors.New("signup request is not pending or was already provisioned")

func (h *Handler) loadSignupIntentForManualProvision(ctx context.Context, intentID uuid.UUID) (*signupIntent, error) {
	var intent signupIntent
	err := h.db.QueryRow(ctx, `
		UPDATE platform.signup_intents
		SET status = 'email_verified',
			email_verified_at = COALESCE(email_verified_at, NOW()),
			updated_at = NOW()
		WHERE id = $1
		  AND status IN ('pending_email_verification', 'email_verified')
		  AND provisioned_tenant_id IS NULL
		RETURNING id, first_name, last_name, email, mobile, password_hash, company_name, subdomain, country, timezone, trial_days
	`, intentID).Scan(
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
			return nil, fmt.Errorf("%w", pgNoRowsLike)
		}
		return nil, fmt.Errorf("load signup request for manual provision: %w", err)
	}
	return &intent, nil
}

type adminEditableSignupIntent struct {
	ID                    uuid.UUID
	FirstName             string
	LastName              string
	Email                 string
	Mobile                string
	CompanyName           string
	Subdomain             string
	Country               string
	Timezone              string
	TrialDays             int32
	Status                string
	VerificationTokenHash string
	EmailVerifiedAt       sql.NullTime
	ExpiresAt             time.Time
	ProvisionedTenantID   sql.NullString
}

func (h *Handler) loadSignupIntentForAdminEdit(ctx context.Context, intentID uuid.UUID) (*adminEditableSignupIntent, error) {
	var item adminEditableSignupIntent
	err := h.db.QueryRow(ctx, `
		SELECT id, first_name, last_name, email, mobile, company_name, subdomain, country, timezone, trial_days,
			status, verification_token_hash, email_verified_at, expires_at, provisioned_tenant_id::text
		FROM platform.signup_intents
		WHERE id = $1
	`, intentID).Scan(
		&item.ID,
		&item.FirstName,
		&item.LastName,
		&item.Email,
		&item.Mobile,
		&item.CompanyName,
		&item.Subdomain,
		&item.Country,
		&item.Timezone,
		&item.TrialDays,
		&item.Status,
		&item.VerificationTokenHash,
		&item.EmailVerifiedAt,
		&item.ExpiresAt,
		&item.ProvisionedTenantID,
	)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (h *Handler) loadSignupIntentResponse(ctx context.Context, intentID uuid.UUID) (*adminSignupIntentResponse, error) {
	rows, err := h.db.Query(ctx, `
		SELECT
			id::text, first_name, last_name, email, mobile, company_name, subdomain, country, timezone, trial_days, status,
			verification_sent_at, email_verified_at, expires_at, provisioned_tenant_id::text, provisioned_user_id::text,
			provisioned_subscription_id::text, created_at, updated_at
		FROM platform.signup_intents
		WHERE id = $1
	`, intentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := h.scanSignupIntentResponses(ctx, rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, pgx.ErrNoRows
	}
	return &items[0], nil
}

type signupIntentRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
}

func (h *Handler) scanSignupIntentResponses(ctx context.Context, rows signupIntentRows) ([]adminSignupIntentResponse, error) {
	requests := make([]adminSignupIntentResponse, 0)
	for rows.Next() {
		var item adminSignupIntentResponse
		var emailVerifiedAt sql.NullTime
		var provisionedTenantID sql.NullString
		var provisionedUserID sql.NullString
		var provisionedSubscriptionID sql.NullString
		if err := rows.Scan(
			&item.ID,
			&item.FirstName,
			&item.LastName,
			&item.Email,
			&item.Mobile,
			&item.CompanyName,
			&item.Subdomain,
			&item.Country,
			&item.Timezone,
			&item.TrialDays,
			&item.Status,
			&item.VerificationSentAt,
			&emailVerifiedAt,
			&item.ExpiresAt,
			&provisionedTenantID,
			&provisionedUserID,
			&provisionedSubscriptionID,
			&item.CreatedAt,
			&item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.TenantURL = "https://" + h.tenantHost(item.Subdomain)
		item.EmailTokenExpired = item.Status == "pending_email_verification" && time.Now().UTC().After(item.ExpiresAt)
		item.EmailVerifiedAt = nullTimePtr(emailVerifiedAt)
		item.ProvisionedTenantID = nullStringPtr(provisionedTenantID)
		item.ProvisionedUserID = nullStringPtr(provisionedUserID)
		item.ProvisionedSubscriptionID = nullStringPtr(provisionedSubscriptionID)
		requests = append(requests, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return requests, nil
}

func (req *adminUpdateSignupIntentRequest) normalize() {
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Mobile = normalizeAssistedProvisionMobile(req.Mobile)
	req.CompanyName = strings.TrimSpace(req.CompanyName)
	req.Subdomain = strings.TrimSpace(strings.ToLower(req.Subdomain))
	req.Country = strings.TrimSpace(strings.ToUpper(req.Country))
	req.Timezone = strings.TrimSpace(req.Timezone)
	if req.Country == "" {
		req.Country = "IN"
	}
	if req.Timezone == "" {
		req.Timezone = "Asia/Kolkata"
	}
	if req.TrialDays == 0 {
		req.TrialDays = defaultAssistedProvisionTrialDays
	}
}

func (req adminUpdateSignupIntentRequest) validate(current *adminEditableSignupIntent) error {
	if req.FirstName == "" || req.LastName == "" || req.Email == "" || req.Mobile == "" || req.CompanyName == "" || req.Subdomain == "" {
		return errors.New("first name, last name, email, mobile, company, and workspace address are required")
	}
	if _, err := mail.ParseAddress(req.Email); err != nil {
		return errors.New("email is invalid")
	}
	if req.TrialDays < 0 || req.TrialDays > 365 {
		return errors.New("trial days must be between 0 and 365")
	}
	if current == nil {
		return errors.New("signup request is invalid")
	}
	return nil
}

func (h *Handler) ensureSignupIntentUpdateUnique(ctx context.Context, intentID uuid.UUID, current *adminEditableSignupIntent, req adminUpdateSignupIntentRequest) error {
	if req.Email != current.Email {
		var exists bool
		if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.users WHERE lower(email) = lower($1))`, req.Email).Scan(&exists); err != nil {
			return fmt.Errorf("check signup email: %w", err)
		}
		if exists {
			return errors.New("email already exists")
		}
		if err := h.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM platform.signup_intents
				WHERE id <> $1
				  AND lower(email) = lower($2)
				  AND status IN ('pending_email_verification', 'email_verified')
				  AND expires_at > NOW()
			)
		`, intentID, req.Email).Scan(&exists); err != nil {
			return fmt.Errorf("check pending signup email: %w", err)
		}
		if exists {
			return errors.New("email is already used by another pending signup request")
		}
	}
	if req.Mobile != current.Mobile {
		var exists bool
		if err := h.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.users WHERE mobile = $1)`, req.Mobile).Scan(&exists); err != nil {
			return fmt.Errorf("check signup mobile: %w", err)
		}
		if exists {
			return errors.New("mobile already exists")
		}
		if err := h.db.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM platform.signup_intents
				WHERE id <> $1
				  AND mobile = $2
				  AND status IN ('pending_email_verification', 'email_verified')
				  AND expires_at > NOW()
			)
		`, intentID, req.Mobile).Scan(&exists); err != nil {
			return fmt.Errorf("check pending signup mobile: %w", err)
		}
		if exists {
			return errors.New("mobile is already used by another pending signup request")
		}
	}
	if req.Subdomain != current.Subdomain {
		if err := h.validateRequestedSignupSubdomain(ctx, req.Subdomain); err != nil {
			return err
		}
	}
	return nil
}

func nullTimePtr(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	return &value.Time
}

func nullStringPtr(value sql.NullString) *string {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return nil
	}
	return &value.String
}
