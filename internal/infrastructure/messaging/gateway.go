package messaging

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

var ErrNotConfigured = errors.New("messaging gateway is not configured")

type Channel string

const (
	ChannelWhatsApp Channel = "whatsapp"
	ChannelSMS      Channel = "sms"
	ChannelEmail    Channel = "email"
)

type MessageType string

const (
	MessageTypeText     MessageType = "text"
	MessageTypeTemplate MessageType = "template"
	MessageTypeMedia    MessageType = "media"
)

type MessageStatus string

type Gateway interface {
	Submit(ctx context.Context, tenantID uuid.UUID, req Request) (*Receipt, error)
	GetResult(ctx context.Context, tenantID, messageID uuid.UUID) (*Result, error)
}

type Request struct {
	Channel          Channel           `json:"channel"`
	Recipient        string            `json:"recipient"`
	MessageType      MessageType       `json:"message_type"`
	Subject          string            `json:"subject,omitempty"`
	HTMLBody         string            `json:"html_body,omitempty"`
	TextBody         string            `json:"text_body,omitempty"`
	TemplateName     string            `json:"template_name,omitempty"`
	TemplateLanguage string            `json:"template_language,omitempty"`
	TemplateParams   map[string]string `json:"template_params,omitempty"`
	MediaURL         string            `json:"media_url,omitempty"`
	MediaType        string            `json:"media_type,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	IdempotencyKey   string            `json:"idempotency_key,omitempty"`
	Priority         string            `json:"priority,omitempty"`
	Category         string            `json:"category,omitempty"`
	CorrelationID    string            `json:"correlation_id,omitempty"`
	CallbackRef      string            `json:"callback_ref,omitempty"`
	FromEmail        string            `json:"from_email,omitempty"`
	FromName         string            `json:"from_name,omitempty"`
	ReplyTo          string            `json:"reply_to,omitempty"`
	CC               []string          `json:"cc,omitempty"`
	BCC              []string          `json:"bcc,omitempty"`
}

type Receipt struct {
	MessageID         uuid.UUID     `json:"message_id"`
	TenantID          uuid.UUID     `json:"tenant_id"`
	Channel           Channel       `json:"channel"`
	Status            MessageStatus `json:"status"`
	Accepted          bool          `json:"accepted"`
	IdempotencyKey    string        `json:"idempotency_key,omitempty"`
	CorrelationID     string        `json:"correlation_id,omitempty"`
	ProviderMessageID string        `json:"provider_message_id,omitempty"`
	CreatedAt         time.Time     `json:"created_at"`
}

type Result struct {
	MessageID         uuid.UUID         `json:"message_id"`
	TenantID          uuid.UUID         `json:"tenant_id"`
	Channel           Channel           `json:"channel"`
	Status            MessageStatus     `json:"status"`
	ProviderMessageID string            `json:"provider_message_id,omitempty"`
	ErrorCode         string            `json:"error_code,omitempty"`
	ErrorMessage      string            `json:"error_message,omitempty"`
	SentAt            *time.Time        `json:"sent_at,omitempty"`
	DeliveredAt       *time.Time        `json:"delivered_at,omitempty"`
	ReadAt            *time.Time        `json:"read_at,omitempty"`
	FailedAt          *time.Time        `json:"failed_at,omitempty"`
	Metadata          map[string]string `json:"metadata,omitempty"`
}
