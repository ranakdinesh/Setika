package messaging

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestTolerantGatewayReturnsErrorWhenMessagingNotConfigured(t *testing.T) {
	log := &stubLogger{}
	gateway := NewTolerantGateway(log)

	_, err := gateway.Submit(context.Background(), uuid.New(), Request{
		Channel:     ChannelEmail,
		Recipient:   "user@example.com",
		MessageType: MessageTypeText,
		Subject:     "Verify your email",
		TextBody:    "Verify",
	})
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("expected ErrNotConfigured, got %v", err)
	}
	if log.warnCount != 1 {
		t.Fatalf("expected one warning log, got %d", log.warnCount)
	}
}

func TestTolerantGatewayTrySubmitSwallowsMissingMessaging(t *testing.T) {
	log := &stubLogger{}
	gateway := NewTolerantGateway(log)

	receipt, configured, err := gateway.TrySubmit(context.Background(), uuid.New(), Request{
		Channel:     ChannelEmail,
		Recipient:   "user@example.com",
		MessageType: MessageTypeText,
		Subject:     "Verify your email",
		TextBody:    "Verify",
	})
	if err != nil {
		t.Fatalf("TrySubmit returned error: %v", err)
	}
	if configured {
		t.Fatalf("expected configured=false")
	}
	if receipt != nil {
		t.Fatalf("expected nil receipt, got %#v", receipt)
	}
	if log.warnCount != 1 {
		t.Fatalf("expected one warning log, got %d", log.warnCount)
	}
}

func TestTolerantGatewayDelegatesWhenConfigured(t *testing.T) {
	log := &stubLogger{}
	delegate := &stubGateway{receipt: &Receipt{
		MessageID: uuid.New(),
		TenantID:  uuid.New(),
		Channel:   ChannelEmail,
		Status:    "queued",
		Accepted:  true,
		CreatedAt: time.Now(),
	}}
	gateway := NewTolerantGateway(log)
	gateway.SetGateway(delegate)

	receipt, err := gateway.Submit(context.Background(), delegate.receipt.TenantID, Request{
		Channel:     ChannelEmail,
		Recipient:   "user@example.com",
		MessageType: MessageTypeText,
		Subject:     "Verify your email",
		TextBody:    "Verify",
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if receipt.MessageID != delegate.receipt.MessageID {
		t.Fatalf("expected delegate receipt %s, got %s", delegate.receipt.MessageID, receipt.MessageID)
	}
	if log.warnCount != 0 {
		t.Fatalf("expected no warnings when configured, got %d", log.warnCount)
	}
}

type stubGateway struct {
	receipt *Receipt
	result  *Result
}

func (g *stubGateway) Submit(context.Context, uuid.UUID, Request) (*Receipt, error) {
	return g.receipt, nil
}

func (g *stubGateway) GetResult(context.Context, uuid.UUID, uuid.UUID) (*Result, error) {
	return g.result, nil
}

type stubLogger struct {
	warnCount int
}

func (l *stubLogger) WarnMessagingNotConfigured(context.Context, string, Channel, error) {
	l.warnCount++
}
