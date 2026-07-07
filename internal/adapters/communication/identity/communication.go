package identitycommunication

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/smtp"
	"strings"
	"time"

	"github.com/ranakdinesh/setika/internal/config"
	"github.com/ranakdinesh/setika/internal/logger"
	identity "github.com/ranakdinesh/spur-identity"
)

type smtpIdentityCommunication struct {
	host      string
	port      int
	username  string
	password  string
	fromEmail string
	fromName  string
	log       *logger.Loggerx
}

type sendGridIdentityCommunication struct {
	apiKey      string
	fromEmail   string
	fromName    string
	sandboxMode bool
	client      *http.Client
	log         *logger.Loggerx
}

type SignupAlertMessage struct {
	FirstName   string
	LastName    string
	Email       string
	Mobile      string
	CompanyName string
	Subdomain   string
	TenantURL   string
	AdminURL    string
}

type SignupWelcomeMessage struct {
	FirstName   string
	Email       string
	CompanyName string
	TenantURL   string
	PlanCode    string
	TrialEndsAt string
}

type SetikaNotificationSender interface {
	SendSignupAlert(ctx context.Context, recipients []string, message SignupAlertMessage) error
	SendSignupWelcome(ctx context.Context, message SignupWelcomeMessage) error
}

func New(cfg config.Config, log *logger.Loggerx) identity.CommunicationPort {
	provider := strings.ToLower(strings.TrimSpace(cfg.EmailProvider))
	if provider == "" {
		if strings.TrimSpace(cfg.SendGridAPIKey) != "" {
			provider = "sendgrid"
		} else {
			provider = "smtp"
		}
	}
	fromEmail := firstNonEmpty(cfg.EmailFromEmail, cfg.SMTPFromEmail)
	fromName := firstNonEmpty(cfg.EmailFromName, cfg.SMTPFromName)
	if provider == "sendgrid" {
		if strings.TrimSpace(cfg.SendGridAPIKey) == "" || fromEmail == "" {
			if log != nil {
				log.Warn(context.Background()).Msg("identity email verification uses console logger because SENDGRID_API_KEY or EMAIL_FROM_EMAIL is not configured")
			}
			return &consoleIdentityCommunication{log: log}
		}
		return &sendGridIdentityCommunication{
			apiKey:      strings.TrimSpace(cfg.SendGridAPIKey),
			fromEmail:   fromEmail,
			fromName:    fromName,
			sandboxMode: cfg.SendGridSandboxMode,
			client:      &http.Client{Timeout: 15 * time.Second},
			log:         log,
		}
	}
	if strings.TrimSpace(cfg.SMTPHost) == "" || fromEmail == "" {
		if log != nil {
			log.Warn(context.Background()).Msg("identity email verification uses console logger because SMTP_HOST or EMAIL_FROM_EMAIL is not configured")
		}
		return &consoleIdentityCommunication{log: log}
	}
	return &smtpIdentityCommunication{
		host:      strings.TrimSpace(cfg.SMTPHost),
		port:      cfg.SMTPPort,
		username:  strings.TrimSpace(cfg.SMTPUsername),
		password:  cfg.SMTPPassword,
		fromEmail: fromEmail,
		fromName:  fromName,
		log:       log,
	}
}

type consoleIdentityCommunication struct {
	log *logger.Loggerx
}

func (c *consoleIdentityCommunication) SendOTP(ctx context.Context, recipient string, channel string, code string) error {
	if c.log != nil {
		c.log.Warn(ctx).Str("recipient", recipient).Str("channel", channel).Str("code", code).Msg("email provider not configured: otp logged for development")
	}
	return nil
}

func (c *consoleIdentityCommunication) SendEmailVerification(ctx context.Context, message identity.EmailVerificationMessage) error {
	if c.log != nil {
		c.log.Warn(ctx).
			Str("recipient", message.Recipient).
			Str("verification_url", message.VerificationURL).
			Msg("email provider not configured: verification link logged for development")
	}
	return nil
}

func (c *consoleIdentityCommunication) SendSignupAlert(ctx context.Context, recipients []string, message SignupAlertMessage) error {
	if c.log != nil {
		c.log.Warn(ctx).
			Strs("recipients", recipients).
			Str("company", message.CompanyName).
			Str("email", message.Email).
			Str("tenant_url", message.TenantURL).
			Msg("email provider not configured: signup alert logged for development")
	}
	return nil
}

func (c *consoleIdentityCommunication) SendSignupWelcome(ctx context.Context, message SignupWelcomeMessage) error {
	if c.log != nil {
		c.log.Warn(ctx).
			Str("recipient", message.Email).
			Str("company", message.CompanyName).
			Str("tenant_url", message.TenantURL).
			Msg("email provider not configured: signup welcome logged for development")
	}
	return nil
}

func (s *smtpIdentityCommunication) SendOTP(ctx context.Context, recipient string, channel string, code string) error {
	if channel != "email" {
		if s.log != nil {
			s.log.Warn(ctx).Str("channel", channel).Str("recipient", recipient).Msg("otp skipped: smtp only supports email")
		}
		return nil
	}
	return s.sendEmail(ctx, recipient, "Your Setika verification code", "", fmt.Sprintf("Your Setika verification code is %s.", code))
}

func (s *smtpIdentityCommunication) SendEmailVerification(ctx context.Context, message identity.EmailVerificationMessage) error {
	subject, htmlBody, textBody := verificationEmailContent(message)
	return s.sendEmail(ctx, message.Recipient, subject, htmlBody, textBody)
}

func (s *smtpIdentityCommunication) SendSignupAlert(ctx context.Context, recipients []string, message SignupAlertMessage) error {
	subject, htmlBody, textBody := signupAlertEmailContent(message)
	for _, recipient := range recipients {
		recipient = strings.TrimSpace(recipient)
		if recipient == "" {
			continue
		}
		if err := s.sendEmail(ctx, recipient, subject, htmlBody, textBody); err != nil {
			return err
		}
	}
	return nil
}

func (s *smtpIdentityCommunication) SendSignupWelcome(ctx context.Context, message SignupWelcomeMessage) error {
	subject, htmlBody, textBody := signupWelcomeEmailContent(message)
	return s.sendEmail(ctx, message.Email, subject, htmlBody, textBody)
}

func (s *sendGridIdentityCommunication) SendOTP(ctx context.Context, recipient string, channel string, code string) error {
	if channel != "email" {
		if s.log != nil {
			s.log.Warn(ctx).Str("channel", channel).Str("recipient", recipient).Msg("otp skipped: sendgrid only supports email")
		}
		return nil
	}
	return s.sendEmail(ctx, recipient, "Your Setika verification code", "", fmt.Sprintf("Your Setika verification code is %s.", code))
}

func (s *sendGridIdentityCommunication) SendEmailVerification(ctx context.Context, message identity.EmailVerificationMessage) error {
	subject, htmlBody, textBody := verificationEmailContent(message)
	return s.sendEmail(ctx, message.Recipient, subject, htmlBody, textBody)
}

func (s *sendGridIdentityCommunication) SendSignupAlert(ctx context.Context, recipients []string, message SignupAlertMessage) error {
	subject, htmlBody, textBody := signupAlertEmailContent(message)
	for _, recipient := range recipients {
		recipient = strings.TrimSpace(recipient)
		if recipient == "" {
			continue
		}
		if err := s.sendEmail(ctx, recipient, subject, htmlBody, textBody); err != nil {
			return err
		}
	}
	return nil
}

func (s *sendGridIdentityCommunication) SendSignupWelcome(ctx context.Context, message SignupWelcomeMessage) error {
	subject, htmlBody, textBody := signupWelcomeEmailContent(message)
	return s.sendEmail(ctx, message.Email, subject, htmlBody, textBody)
}

func verificationEmailContent(message identity.EmailVerificationMessage) (string, string, string) {
	name := strings.TrimSpace(message.FirstName)
	if name == "" {
		name = "there"
	}
	escapedName := html.EscapeString(name)
	escapedURL := html.EscapeString(message.VerificationURL)
	logoURL := html.EscapeString(verificationEmailLogoURL(message.VerificationURL))
	preheader := "Verify your email to activate your Setika trial workspace."
	htmlBody := fmt.Sprintf(
		`<!doctype html>
<html>
  <body style="margin:0;background:#f6f4ec;font-family:'Plus Jakarta Sans','Segoe UI',Arial,sans-serif;color:#172033;">
    <span style="display:none!important;visibility:hidden;opacity:0;height:0;width:0;overflow:hidden;">%s</span>
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#f6f4ec;padding:32px 16px;">
      <tr>
        <td align="center">
          <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:560px;background:#ffffff;border:1px solid #e6eadf;border-radius:18px;overflow:hidden;">
            <tr>
              <td style="background:#588368;padding:26px 30px;color:#ffffff;">
                <div style="display:flex;align-items:center;gap:12px;">
                  <img src="%s" width="145" height="37" alt="Setika" style="display:block;height:37px;width:145px;max-width:145px;background:#ffffff;border-radius:10px;padding:8px;" />
                </div>
                <h1 style="margin:10px 0 0;font-size:28px;line-height:1.2;font-weight:800;">Activate your HR workspace</h1>
              </td>
            </tr>
            <tr>
              <td style="padding:30px;">
                <p style="margin:0 0 16px;font-size:16px;line-height:1.6;">Hello %s,</p>
                <p style="margin:0 0 22px;font-size:16px;line-height:1.6;color:#4b5563;">Verify your email address to start your Setika 30-day trial. Your tenant workspace will be created after verification.</p>
                <a href="%s" style="display:inline-block;background:#e87839;color:#ffffff;text-decoration:none;border-radius:12px;padding:14px 22px;font-weight:800;font-size:15px;">Verify email and create workspace</a>
                <p style="margin:24px 0 0;font-size:13px;line-height:1.6;color:#6b7280;">If the button does not work, open this link:<br><a href="%s" style="word-break:break-all;color:#588368;text-decoration:underline;">%s</a></p>
                <p style="margin:22px 0 0;font-size:13px;line-height:1.6;color:#6b7280;">If you did not request this Setika trial, you can ignore this email.</p>
              </td>
            </tr>
          </table>
        </td>
      </tr>
    </table>
  </body>
</html>`,
		preheader,
		logoURL,
		escapedName,
		escapedURL,
		escapedURL,
		escapedURL,
	)
	textBody := fmt.Sprintf("Hello %s,\n\nVerify your email address to start your Setika 30-day trial and create your tenant workspace:\n%s\n\nIf you did not request this Setika trial, you can ignore this email.", name, message.VerificationURL)
	return "Verify your Setika email address", htmlBody, textBody
}

func signupAlertEmailContent(message SignupAlertMessage) (string, string, string) {
	fullName := strings.TrimSpace(message.FirstName + " " + message.LastName)
	if fullName == "" {
		fullName = "Customer"
	}
	escapedCompany := html.EscapeString(message.CompanyName)
	escapedName := html.EscapeString(fullName)
	escapedEmail := html.EscapeString(message.Email)
	escapedMobile := html.EscapeString(message.Mobile)
	escapedTenantURL := html.EscapeString(message.TenantURL)
	escapedAdminURL := html.EscapeString(message.AdminURL)
	htmlBody := fmt.Sprintf(`<!doctype html>
<html>
  <body style="margin:0;background:#f6f4ec;font-family:'Plus Jakarta Sans','Segoe UI',Arial,sans-serif;color:#172033;">
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#f6f4ec;padding:28px 16px;">
      <tr><td align="center">
        <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:560px;background:#ffffff;border:1px solid #e6eadf;border-radius:18px;overflow:hidden;">
          <tr><td style="background:#588368;padding:24px 28px;color:#ffffff;"><img src="https://www.setika.one/assets/img/logo.png" width="138" alt="Setika" style="display:block;background:#ffffff;border-radius:10px;padding:8px;" /><h1 style="margin:16px 0 0;font-size:24px;line-height:1.25;">New signup request</h1></td></tr>
          <tr><td style="padding:28px;">
            <p style="margin:0 0 16px;font-size:15px;line-height:1.6;color:#4b5563;">A customer has requested a Setika trial workspace.</p>
            <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="font-size:14px;line-height:1.6;color:#374151;">
              <tr><td style="padding:6px 0;font-weight:800;">Company</td><td style="padding:6px 0;">%s</td></tr>
              <tr><td style="padding:6px 0;font-weight:800;">Contact</td><td style="padding:6px 0;">%s</td></tr>
              <tr><td style="padding:6px 0;font-weight:800;">Email</td><td style="padding:6px 0;">%s</td></tr>
              <tr><td style="padding:6px 0;font-weight:800;">Mobile</td><td style="padding:6px 0;">%s</td></tr>
              <tr><td style="padding:6px 0;font-weight:800;">Workspace</td><td style="padding:6px 0;">%s</td></tr>
            </table>
            <a href="%s" style="display:inline-block;margin-top:18px;background:#e87839;color:#ffffff;text-decoration:none;border-radius:12px;padding:13px 20px;font-weight:800;font-size:14px;">Open signup requests</a>
          </td></tr>
        </table>
      </td></tr>
    </table>
  </body>
</html>`, escapedCompany, escapedName, escapedEmail, escapedMobile, escapedTenantURL, escapedAdminURL)
	textBody := fmt.Sprintf("New Setika signup request\n\nCompany: %s\nContact: %s\nEmail: %s\nMobile: %s\nWorkspace: %s\n\nOpen signup requests: %s", message.CompanyName, fullName, message.Email, message.Mobile, message.TenantURL, message.AdminURL)
	return "New Setika signup request", htmlBody, textBody
}

func signupWelcomeEmailContent(message SignupWelcomeMessage) (string, string, string) {
	name := strings.TrimSpace(message.FirstName)
	if name == "" {
		name = "there"
	}
	escapedName := html.EscapeString(name)
	escapedCompany := html.EscapeString(message.CompanyName)
	escapedTenantURL := html.EscapeString(message.TenantURL)
	escapedPlan := html.EscapeString(message.PlanCode)
	escapedTrialEnds := html.EscapeString(message.TrialEndsAt)
	htmlBody := fmt.Sprintf(`<!doctype html>
<html>
  <body style="margin:0;background:#f6f4ec;font-family:'Plus Jakarta Sans','Segoe UI',Arial,sans-serif;color:#172033;">
    <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="background:#f6f4ec;padding:32px 16px;">
      <tr><td align="center">
        <table role="presentation" width="100%%" cellspacing="0" cellpadding="0" style="max-width:580px;background:#ffffff;border:1px solid #e6eadf;border-radius:18px;overflow:hidden;">
          <tr><td style="background:#588368;padding:26px 30px;color:#ffffff;"><img src="https://www.setika.one/assets/img/logo.png" width="145" alt="Setika" style="display:block;background:#ffffff;border-radius:10px;padding:8px;" /><h1 style="margin:18px 0 0;font-size:28px;line-height:1.2;">Your Setika workspace is ready</h1></td></tr>
          <tr><td style="padding:30px;">
            <p style="margin:0 0 16px;font-size:16px;line-height:1.6;">Hello %s,</p>
            <p style="margin:0 0 20px;font-size:16px;line-height:1.6;color:#4b5563;">%s is now active on Setika. Your trial workspace is ready for setup.</p>
            <a href="%s" style="display:inline-block;background:#e87839;color:#ffffff;text-decoration:none;border-radius:12px;padding:14px 22px;font-weight:800;font-size:15px;">Open workspace</a>
            <div style="margin-top:24px;padding:18px;border-radius:14px;background:#f8faf9;border:1px solid #e6eadf;">
              <p style="margin:0 0 10px;font-weight:800;color:#172033;">Included to help you get started</p>
              <ul style="margin:0;padding-left:20px;color:#4b5563;line-height:1.7;font-size:14px;">
                <li>Employee records and organization setup</li>
                <li>Attendance, leave, payroll, documents, hiring, and onboarding foundations</li>
                <li>Reports and role-based access controls for your HR team</li>
              </ul>
            </div>
            <p style="margin:22px 0 0;font-size:13px;line-height:1.6;color:#6b7280;">Plan: %s%s</p>
          </td></tr>
        </table>
      </td></tr>
    </table>
  </body>
</html>`, escapedName, escapedCompany, escapedTenantURL, escapedPlan, trialEndsSuffix(escapedTrialEnds))
	textBody := fmt.Sprintf("Hello %s,\n\n%s is now active on Setika. Open your workspace: %s\n\nIncluded to help you get started:\n- Employee records and organization setup\n- Attendance, leave, payroll, documents, hiring, and onboarding foundations\n- Reports and role-based access controls for your HR team\n\nPlan: %s%s", name, message.CompanyName, message.TenantURL, message.PlanCode, trialEndsSuffix(message.TrialEndsAt))
	return "Your Setika workspace is ready", htmlBody, textBody
}

func trialEndsSuffix(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return " · Trial ends " + value
}

func verificationEmailLogoURL(verificationURL string) string {
	return "https://www.setika.one/assets/img/logo.png"
}

func (s *smtpIdentityCommunication) sendEmail(ctx context.Context, recipient string, subject string, htmlBody string, textBody string) error {
	addr := fmt.Sprintf("%s:%d", s.host, s.port)
	headers := map[string]string{
		"From":         formatAddress(s.fromName, s.fromEmail),
		"To":           recipient,
		"Subject":      subject,
		"MIME-Version": "1.0",
	}
	contentType := "text/plain; charset=UTF-8"
	body := textBody
	if strings.TrimSpace(htmlBody) != "" {
		contentType = "text/html; charset=UTF-8"
		body = htmlBody
	}
	headers["Content-Type"] = contentType

	var raw strings.Builder
	for key, value := range headers {
		raw.WriteString(key)
		raw.WriteString(": ")
		raw.WriteString(value)
		raw.WriteString("\r\n")
	}
	raw.WriteString("\r\n")
	raw.WriteString(body)

	var auth smtp.Auth
	if s.username != "" {
		auth = smtp.PlainAuth("", s.username, s.password, s.host)
	}
	if err := smtp.SendMail(addr, auth, s.fromEmail, []string{recipient}, []byte(raw.String())); err != nil {
		if s.log != nil {
			s.log.Error(ctx).Err(err).Str("recipient", recipient).Str("smtp_host", s.host).Msg("smtp email dispatch failed")
		}
		return err
	}
	if s.log != nil {
		s.log.Info(ctx).Str("recipient", recipient).Str("subject", subject).Msg("smtp email dispatched")
	}
	return nil
}

func (s *sendGridIdentityCommunication) sendEmail(ctx context.Context, recipient string, subject string, htmlBody string, textBody string) error {
	body := map[string]any{
		"personalizations": []map[string]any{{"to": []map[string]string{{"email": recipient}}}},
		"from":             map[string]string{"email": s.fromEmail, "name": s.fromName},
		"subject":          subject,
		"content":          []map[string]string{{"type": "text/plain", "value": textBody}},
	}
	if strings.TrimSpace(htmlBody) != "" {
		body["content"] = append(body["content"].([]map[string]string), map[string]string{"type": "text/html", "value": htmlBody})
	}
	if s.sandboxMode {
		body["mail_settings"] = map[string]any{"sandbox_mode": map[string]bool{"enable": true}}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.sendgrid.com/v3/mail/send", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		if s.log != nil {
			s.log.Error(ctx).Err(err).Str("recipient", recipient).Msg("sendgrid email dispatch failed")
		}
		return err
	}
	defer resp.Body.Close()
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		err := fmt.Errorf("sendgrid delivery failed: status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
		if s.log != nil {
			s.log.Error(ctx).Err(err).Str("recipient", recipient).Msg("sendgrid email dispatch rejected")
		}
		return err
	}
	if s.log != nil {
		s.log.Info(ctx).Str("recipient", recipient).Str("subject", subject).Msg("sendgrid email dispatched")
	}
	return nil
}

func formatAddress(name string, email string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return email
	}
	return fmt.Sprintf("%s <%s>", name, email)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
