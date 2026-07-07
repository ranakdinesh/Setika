package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/ranakdinesh/spur-identity/adapters/http/httputil"
	identitydb "github.com/ranakdinesh/spur-identity/adapters/postgres/db"
)

type adminTenantResponse struct {
	ID               uuid.UUID  `json:"id"`
	Name             string     `json:"name"`
	Kind             string     `json:"kind"`
	Subdomain        *string    `json:"subdomain,omitempty"`
	DisplayName      *string    `json:"display_name,omitempty"`
	TrialEndsAt      *time.Time `json:"trial_ends_at,omitempty"`
	SubscriptionPlan *string    `json:"subscription_plan,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
	AdminUserID      *uuid.UUID `json:"admin_user_id,omitempty"`
	AdminName        *string    `json:"admin_name,omitempty"`
	AdminEmail       *string    `json:"admin_email,omitempty"`
	AdminMobile      *string    `json:"admin_mobile,omitempty"`
}

func (h *Handler) AdminListTenants(w http.ResponseWriter, r *http.Request) {
	if !httputil.IsSuperAdmin(r.Context()) {
		writeError(w, http.StatusForbidden, "super admin access required")
		return
	}
	if h == nil || h.db == nil {
		writeError(w, http.StatusInternalServerError, "admin tenant handler is not configured")
		return
	}

	tenants := make([]adminTenantResponse, 0)
	if err := identitydb.RunInTx(r.Context(), h.db, func(txCtx context.Context) error {
		tx := identitydb.GetTx(txCtx)
		if tx == nil {
			return errors.New("identity transaction is not available")
		}
		if _, err := tx.Exec(txCtx, "SELECT set_config('app.tenant_id', '', true), set_config('app.is_super_admin', 'true', true)"); err != nil {
			return err
		}

		rows, err := tx.Query(txCtx, `
			SELECT
				t.id,
				t.name,
				t.kind::text,
				t.trial_ends_at,
				t.subscription_plan,
				t.created_at,
				t.updated_at,
				tp.subdomain,
				tp.display_name,
				admin_user.id,
				NULLIF(btrim(concat_ws(' ', admin_user.first_name, admin_user.last_name)), ''),
				admin_user.email,
				admin_user.mobile
			FROM auth.tenants t
			LEFT JOIN hrms.tenant_profiles tp ON tp.tenant_id = t.id
			LEFT JOIN LATERAL (
				SELECT
					u.id,
					u.first_name,
					u.last_name,
					u.email,
					u.mobile,
					CASE
						WHEN EXISTS (
							SELECT 1
							FROM auth.user_roles ur
							JOIN auth.roles r ON r.id = ur.role_id
							WHERE ur.user_id = u.id
							  AND r.tenant_id = t.id
							  AND r.code = 'TENANT_ADMIN'
						) THEN 0
						ELSE 1
					END AS admin_rank
				FROM auth.users u
				WHERE u.tenant_id = t.id
				  AND u.is_active = true
				ORDER BY admin_rank, u.created_at ASC
				LIMIT 1
			) admin_user ON true
			ORDER BY t.created_at DESC
		`)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var tenant adminTenantResponse
			var trialEndsAt sql.NullTime
			var subscriptionPlan sql.NullString
			var subdomain sql.NullString
			var displayName sql.NullString
			var adminUserID pgtype.UUID
			var adminName sql.NullString
			var adminEmail sql.NullString
			var adminMobile sql.NullString
			if err := rows.Scan(
				&tenant.ID,
				&tenant.Name,
				&tenant.Kind,
				&trialEndsAt,
				&subscriptionPlan,
				&tenant.CreatedAt,
				&tenant.UpdatedAt,
				&subdomain,
				&displayName,
				&adminUserID,
				&adminName,
				&adminEmail,
				&adminMobile,
			); err != nil {
				return err
			}
			if trialEndsAt.Valid {
				tenant.TrialEndsAt = &trialEndsAt.Time
			}
			if subscriptionPlan.Valid {
				tenant.SubscriptionPlan = &subscriptionPlan.String
			}
			tenant.Subdomain = nullableString(subdomain)
			tenant.DisplayName = nullableString(displayName)
			tenant.AdminUserID = nullableUUID(adminUserID)
			tenant.AdminName = nullableString(adminName)
			tenant.AdminEmail = nullableString(adminEmail)
			tenant.AdminMobile = nullableString(adminMobile)
			tenants = append(tenants, tenant)
		}
		return rows.Err()
	}); err != nil {
		if h.log != nil {
			h.log.Error(r.Context()).Err(err).Str("operation", "admin list tenants").Msg("admin tenant list failed")
		}
		writeError(w, http.StatusInternalServerError, "failed to load tenants")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(tenants)
}

func nullableString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	return &value.String
}

func nullableUUID(value pgtype.UUID) *uuid.UUID {
	if !value.Valid {
		return nil
	}
	id, err := uuid.FromBytes(value.Bytes[:])
	if err != nil {
		return nil
	}
	return &id
}
