package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type adminUpdateTenantUserRequest struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Mobile    string `json:"mobile"`
	IsActive  *bool  `json:"is_active"`
}

func (h *Handler) AdminUpdateTenantUser(w http.ResponseWriter, r *http.Request) {
	tenantID, err := uuid.Parse(chi.URLParam(r, "tenantID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid tenant id")
		return
	}
	userID, err := uuid.Parse(chi.URLParam(r, "userID"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid user id")
		return
	}

	var req adminUpdateTenantUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.FirstName = strings.TrimSpace(req.FirstName)
	req.LastName = strings.TrimSpace(req.LastName)
	req.Mobile = strings.TrimSpace(req.Mobile)
	if req.FirstName == "" || req.LastName == "" || req.Mobile == "" {
		writeError(w, http.StatusBadRequest, "first name, last name, and mobile are required")
		return
	}

	isActiveExpr := "is_active"
	args := []any{req.FirstName, req.LastName, req.Mobile, userID, tenantID}
	if req.IsActive != nil {
		isActiveExpr = "$6"
		args = append(args, *req.IsActive)
	}

	if h == nil || h.db == nil {
		writeError(w, http.StatusInternalServerError, "admin user handler is not configured")
		return
	}

	tag, err := h.db.Exec(r.Context(), `
		UPDATE auth.users
		SET first_name = $1,
			last_name = $2,
			mobile = $3,
			is_active = `+isActiveExpr+`,
			updated_at = NOW()
		WHERE id = $4 AND tenant_id = $5
	`, args...)
	if err != nil {
		if h.log != nil {
			h.log.Error(r.Context()).Err(err).Str("operation", "admin update tenant user").Msg("admin tenant user update failed")
		}
		writeError(w, http.StatusInternalServerError, "failed to update user")
		return
	}
	if tag.RowsAffected() == 0 {
		writeError(w, http.StatusNotFound, "user not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
