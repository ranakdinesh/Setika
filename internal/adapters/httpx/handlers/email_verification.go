package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	identitydb "github.com/ranakdinesh/spur-identity/adapters/postgres/db"
)

type resendEmailVerificationRequest struct {
	Email string `json:"email"`
}

func (h *Handler) ResendEmailVerification(w http.ResponseWriter, r *http.Request) {
	var req resendEmailVerificationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.logSignupError(r.Context(), "decode resend verification request", err)
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	email := strings.TrimSpace(strings.ToLower(req.Email))
	if email == "" {
		err := errors.New("email is required")
		h.logSignupError(r.Context(), "validate resend verification request", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if h == nil || h.identity == nil || h.identity.DB == nil || h.identity.Services == nil || h.identity.Services.AuthService == nil {
		err := errors.New("email verification resend is not configured")
		h.logSignupError(r.Context(), "validate resend verification dependencies", err)
		writeError(w, http.StatusInternalServerError, "email verification resend is not configured")
		return
	}

	if err := identitydb.RunInTx(r.Context(), h.identity.DB, func(txCtx context.Context) error {
		tx := identitydb.GetTx(txCtx)
		if tx == nil {
			return errors.New("identity transaction is not available")
		}
		if _, err := tx.Exec(txCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			return err
		}
		if _, err := tx.Exec(txCtx, `
			UPDATE auth.verification_challenges
			SET consumed_at = NOW()
			WHERE kind = 'email_verify'
			  AND consumed_at IS NULL
			  AND user_id IN (
				  SELECT id FROM auth.users WHERE lower(email) = lower($1)
			  )
		`, email); err != nil {
			return err
		}
		return h.identity.Services.AuthService.ResendEmailVerification(txCtx, email)
	}); err != nil {
		h.logSignupError(r.Context(), "resend email verification", err)
		writeError(w, http.StatusInternalServerError, "failed to resend verification email")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}
