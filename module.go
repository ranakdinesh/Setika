package template

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
)

var (
	ErrMessagingNotConfigured = errors.New("template: messaging gateway is not configured")
	ErrStorageNotConfigured   = errors.New("template: storage gateway is not configured")
)

type Options struct {
	Messaging MessageGateway
	Storage   FileStorage
	Logger    Logger
}

type Module struct {
	Services *Services
}

type Services struct {
	IdentityNotifications *IdentityNotificationService
	FileStore             *FileStoreService
	EngageMessages        *EngageMessageService
}

func New(_ context.Context, opt Options) (*Module, error) {
	return &Module{Services: &Services{
		IdentityNotifications: &IdentityNotificationService{messaging: opt.Messaging, logger: opt.Logger},
		FileStore:             &FileStoreService{storage: opt.Storage},
		EngageMessages:        &EngageMessageService{messaging: opt.Messaging, logger: opt.Logger},
	}}, nil
}

type Logger interface {
	Warn(ctx context.Context, message string, fields map[string]any)
}

type MessageGateway interface {
	Submit(ctx context.Context, tenantID uuid.UUID, req MessageRequest) (*MessageReceipt, error)
}

type FileStorage interface {
	Store(ctx context.Context, input StoreFileInput) (*StoredFile, error)
}

type Channel string

const (
	ChannelEmail    Channel = "email"
	ChannelSMS      Channel = "sms"
	ChannelWhatsApp Channel = "whatsapp"
)

type MessageType string

const (
	MessageTypeText MessageType = "text"
)

type MessageRequest struct {
	Channel        Channel           `json:"channel"`
	Recipient      string            `json:"recipient"`
	MessageType    MessageType       `json:"message_type"`
	Subject        string            `json:"subject,omitempty"`
	HTMLBody       string            `json:"html_body,omitempty"`
	TextBody       string            `json:"text_body,omitempty"`
	Category       string            `json:"category,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	CorrelationID  string            `json:"correlation_id,omitempty"`
	Metadata       map[string]string `json:"metadata,omitempty"`
}

type MessageReceipt struct {
	MessageID         uuid.UUID `json:"message_id"`
	Accepted          bool      `json:"accepted"`
	Status            string    `json:"status"`
	IdempotencyKey    string    `json:"idempotency_key,omitempty"`
	CorrelationID     string    `json:"correlation_id,omitempty"`
	ProviderMessageID string    `json:"provider_message_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

type EmailVerificationInput struct {
	TenantID        uuid.UUID
	UserID          uuid.UUID
	Recipient       string
	FirstName       string
	VerificationURL string
	TemplateKey     string
}

type EngageReplyInput struct {
	TenantID       uuid.UUID
	ThreadID       uuid.UUID
	Channel        Channel
	Recipient      string
	TextBody       string
	Subject        string
	IdempotencyKey string
	CorrelationID  string
	Metadata       map[string]string
}

type EngageReplyReceipt struct {
	MessageID         uuid.UUID
	Accepted          bool
	Status            string
	IdempotencyKey    string
	CorrelationID     string
	ProviderMessageID string
	CreatedAt         time.Time
}

type EngageMessageService struct {
	messaging MessageGateway
	logger    Logger
}

func (s *EngageMessageService) SendReply(ctx context.Context, input EngageReplyInput) (*EngageReplyReceipt, error) {
	if s.messaging == nil {
		return nil, ErrMessagingNotConfigured
	}
	metadata := map[string]string{
		"source":    "engage",
		"kind":      "inbox_reply",
		"thread_id": input.ThreadID.String(),
	}
	for k, v := range input.Metadata {
		if k != "" && v != "" {
			metadata[k] = v
		}
	}
	idempotencyKey := strings.TrimSpace(input.IdempotencyKey)
	if idempotencyKey == "" {
		sum := sha256.Sum256([]byte(input.TenantID.String() + ":" + input.ThreadID.String() + ":" + input.Recipient + ":" + input.TextBody))
		idempotencyKey = "engage:inbox-reply:" + hex.EncodeToString(sum[:12])
	}
	correlationID := strings.TrimSpace(input.CorrelationID)
	if correlationID == "" {
		correlationID = "engage.inbox_reply"
	}
	receipt, err := s.messaging.Submit(ctx, input.TenantID, MessageRequest{
		Channel:        input.Channel,
		Recipient:      input.Recipient,
		MessageType:    MessageTypeText,
		Subject:        input.Subject,
		TextBody:       input.TextBody,
		Category:       "conversation",
		IdempotencyKey: idempotencyKey,
		CorrelationID:  correlationID,
		Metadata:       metadata,
	})
	if err != nil {
		return nil, err
	}
	return &EngageReplyReceipt{
		MessageID:         receipt.MessageID,
		Accepted:          receipt.Accepted,
		Status:            receipt.Status,
		IdempotencyKey:    receipt.IdempotencyKey,
		CorrelationID:     receipt.CorrelationID,
		ProviderMessageID: receipt.ProviderMessageID,
		CreatedAt:         receipt.CreatedAt,
	}, nil
}

type IdentityNotificationService struct {
	messaging MessageGateway
	logger    Logger
}

func (s *IdentityNotificationService) SendOTP(ctx context.Context, tenantID uuid.UUID, recipient string, channel Channel, code string) error {
	if s.messaging == nil {
		s.warn(ctx, "otp skipped because messaging is not configured", map[string]any{
			"recipient": recipient,
			"channel":   string(channel),
			"kind":      "otp",
		})
		return nil
	}
	subject := ""
	text := fmt.Sprintf("Your Citual verification code is %s.", code)
	if channel == ChannelEmail {
		subject = "Your Citual verification code"
	}
	_, err := s.messaging.Submit(ctx, tenantID, MessageRequest{
		Channel:        channel,
		Recipient:      recipient,
		MessageType:    MessageTypeText,
		Subject:        subject,
		TextBody:       text,
		Category:       "transactional",
		IdempotencyKey: "identity:otp:" + recipient + ":" + code,
		Metadata: map[string]string{
			"source": "identity",
			"kind":   "otp",
		},
	})
	if err != nil {
		s.warn(ctx, "otp dispatch failed", map[string]any{
			"recipient": recipient,
			"channel":   string(channel),
			"error":     err.Error(),
		})
		return nil
	}
	return nil
}

func (s *IdentityNotificationService) SendEmailVerification(ctx context.Context, input EmailVerificationInput) error {
	if s.messaging == nil {
		s.warn(ctx, "email verification skipped because messaging is not configured", map[string]any{
			"recipient": input.Recipient,
			"kind":      "email_verification",
		})
		return nil
	}
	name := strings.TrimSpace(input.FirstName)
	if name == "" {
		name = "there"
	}
	escapedName := html.EscapeString(name)
	escapedURL := html.EscapeString(input.VerificationURL)
	htmlBody := fmt.Sprintf(
		`<p>Hello %s,</p><p>Please verify your email address to activate your Citual account.</p><p><a href="%s">Verify email</a></p><p>If you did not request this, you can ignore this message.</p>`,
		escapedName,
		escapedURL,
	)
	textBody := fmt.Sprintf("Hello %s,\n\nPlease verify your email address to activate your Citual account:\n%s\n\nIf you did not request this, you can ignore this message.", name, input.VerificationURL)
	verificationHash := sha256.Sum256([]byte(input.VerificationURL))
	_, err := s.messaging.Submit(ctx, input.TenantID, MessageRequest{
		Channel:        ChannelEmail,
		Recipient:      input.Recipient,
		MessageType:    MessageTypeText,
		Subject:        "Verify your Citual email address",
		HTMLBody:       htmlBody,
		TextBody:       textBody,
		Category:       "transactional",
		IdempotencyKey: "identity:email-verification:" + input.Recipient + ":" + hex.EncodeToString(verificationHash[:8]),
		CorrelationID:  "identity.email_verification",
		Metadata: map[string]string{
			"source":       "identity",
			"kind":         "email_verification",
			"template_key": input.TemplateKey,
		},
	})
	if err != nil {
		s.warn(ctx, "email verification dispatch failed", map[string]any{
			"recipient": input.Recipient,
			"error":     err.Error(),
		})
		return nil
	}
	return nil
}

func (s *IdentityNotificationService) warn(ctx context.Context, message string, fields map[string]any) {
	if s.logger != nil {
		s.logger.Warn(ctx, message, fields)
	}
}

type FilePurpose string

const (
	FilePurposeUserAvatar    FilePurpose = "user_avatar"
	FilePurposeTenantLogo    FilePurpose = "tenant_logo"
	FilePurposeTenantFavicon FilePurpose = "tenant_favicon"
)

type StoreFileInput struct {
	TenantID    uuid.UUID
	UserID      uuid.UUID
	Purpose     FilePurpose
	FileName    string
	ContentType string
	Content     io.Reader
}

type StoredFile struct {
	ObjectID    uuid.UUID `json:"object_id"`
	ObjectKey   string    `json:"object_key"`
	Bucket      string    `json:"bucket"`
	ContentType string    `json:"content_type"`
	SizeBytes   int64     `json:"size_bytes"`
}

type FileStoreService struct {
	storage FileStorage
}

func (s *FileStoreService) StoreAvatar(ctx context.Context, input StoreFileInput) (*StoredFile, error) {
	if s.storage == nil {
		return nil, ErrStorageNotConfigured
	}
	input.Purpose = FilePurposeUserAvatar
	return s.storage.Store(ctx, input)
}

func (s *FileStoreService) StoreTenantBrandAsset(ctx context.Context, input StoreFileInput) (*StoredFile, error) {
	if s.storage == nil {
		return nil, ErrStorageNotConfigured
	}
	switch input.Purpose {
	case FilePurposeTenantLogo, FilePurposeTenantFavicon:
	default:
		input.Purpose = FilePurposeTenantLogo
	}
	return s.storage.Store(ctx, input)
}
