package handlers

import (
	"context"
	"net/mail"
	"strings"
	"time"

	identitycommunication "github.com/ranakdinesh/setika/internal/adapters/communication/identity"
)

func (h *Handler) notifySuperAdminsOfSignupIntent(ctx context.Context, intent signupIntentEmail, companyName, mobile, subdomain string) {
	sender, ok := h.signupNotifier()
	if !ok {
		return
	}
	recipients, err := h.superAdminNotificationRecipients(ctx)
	if err != nil {
		h.logSignupError(ctx, "load super admin signup alert recipients", err)
		return
	}
	if len(recipients) == 0 {
		return
	}
	if err := sender.SendSignupAlert(ctx, recipients, identitycommunication.SignupAlertMessage{
		FirstName:   intent.FirstName,
		Email:       intent.Email,
		Mobile:      mobile,
		CompanyName: companyName,
		Subdomain:   subdomain,
		TenantURL:   "https://" + h.tenantHost(subdomain),
		AdminURL:    h.dashboardSignupRequestsURL(),
	}); err != nil {
		h.logSignupError(ctx, "send super admin signup alert", err)
	}
}

func (h *Handler) sendSignupWelcomeEmail(ctx context.Context, intent *signupIntent, result *adminProvisionTenantResponse) {
	if intent == nil || result == nil {
		return
	}
	sender, ok := h.signupNotifier()
	if !ok {
		return
	}
	trialEndsAt := ""
	if result.TrialEndsAt != nil {
		trialEndsAt = result.TrialEndsAt.Format("2 Jan 2006")
	}
	if err := sender.SendSignupWelcome(ctx, identitycommunication.SignupWelcomeMessage{
		FirstName:   intent.FirstName,
		Email:       intent.Email,
		CompanyName: intent.CompanyName,
		TenantURL:   result.TenantURL,
		PlanCode:    result.PlanCode,
		TrialEndsAt: trialEndsAt,
	}); err != nil {
		h.logSignupError(ctx, "send signup welcome email", err)
	}
}

func (h *Handler) signupNotifier() (identitycommunication.SetikaNotificationSender, bool) {
	if h == nil || h.communication == nil {
		return nil, false
	}
	sender, ok := h.communication.(identitycommunication.SetikaNotificationSender)
	return sender, ok
}

func (h *Handler) superAdminNotificationRecipients(ctx context.Context) ([]string, error) {
	recipients := make([]string, 0)
	seen := make(map[string]struct{})
	addRecipient := func(email string) {
		email = strings.TrimSpace(strings.ToLower(email))
		if !validEmailAddress(email) {
			return
		}
		if _, ok := seen[email]; ok {
			return
		}
		seen[email] = struct{}{}
		recipients = append(recipients, email)
	}
	for _, email := range fallbackSignupAlertRecipients(h.signupAlertEmail) {
		addRecipient(email)
	}
	if h == nil || h.db == nil {
		return recipients, nil
	}
	rows, err := h.db.Query(ctx, `
		SELECT DISTINCT lower(email)
		FROM auth.users
		WHERE is_super_admin = TRUE
		  AND is_active = TRUE
		  AND email IS NOT NULL
		  AND trim(email) <> ''
		ORDER BY lower(email)
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var email string
		if err := rows.Scan(&email); err != nil {
			return nil, err
		}
		addRecipient(email)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return recipients, nil
}

func fallbackSignupAlertRecipients(email string) []string {
	email = strings.TrimSpace(strings.ToLower(email))
	if !validEmailAddress(email) {
		return nil
	}
	return []string{email}
}

func validEmailAddress(email string) bool {
	if strings.TrimSpace(email) == "" {
		return false
	}
	_, err := mail.ParseAddress(email)
	return err == nil
}

func (h *Handler) dashboardSignupRequestsURL() string {
	base := strings.TrimRight(strings.TrimSpace(h.frontendURL), "/")
	if base == "" {
		base = "http://localhost:3000"
	}
	return base + "/dashboard?section=signup-requests"
}

func signupTokenExpiry() time.Time {
	return time.Now().UTC().Add(24 * time.Hour)
}
