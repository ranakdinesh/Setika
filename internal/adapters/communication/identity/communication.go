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

func verificationEmailContent(message identity.EmailVerificationMessage) (string, string, string) {
	name := strings.TrimSpace(message.FirstName)
	if name == "" {
		name = "there"
	}
	escapedName := html.EscapeString(name)
	escapedURL := html.EscapeString(message.VerificationURL)
	htmlBody := fmt.Sprintf(
		`<p>Hello %s,</p><p>Please verify your email address to activate your Setika account.</p><p><a href="%s">Verify email</a></p><p>If you did not request this account, you can ignore this email.</p>`,
		escapedName,
		escapedURL,
	)
	textBody := fmt.Sprintf("Hello %s,\n\nPlease verify your email address to activate your Setika account:\n%s\n\nIf you did not request this account, you can ignore this email.", name, message.VerificationURL)
	return "Verify your Setika email address", htmlBody, textBody
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
