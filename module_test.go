package template

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestIdentityNotificationSkipsWhenMessagingMissing(t *testing.T) {
	mod, err := New(context.Background(), Options{})
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	err = mod.Services.IdentityNotifications.SendEmailVerification(context.Background(), EmailVerificationInput{
		TenantID:        uuid.New(),
		UserID:          uuid.New(),
		Recipient:       "person@example.com",
		FirstName:       "Person",
		VerificationURL: "https://example.com/verify",
	})
	if err != nil {
		t.Fatalf("expected tolerant nil error, got %v", err)
	}
}

func TestFileStoreReturnsErrorWhenStorageMissing(t *testing.T) {
	mod, err := New(context.Background(), Options{})
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	_, err = mod.Services.FileStore.StoreAvatar(context.Background(), StoreFileInput{
		TenantID:    uuid.New(),
		UserID:      uuid.New(),
		FileName:    "avatar.png",
		ContentType: "image/png",
		Content:     strings.NewReader("avatar"),
	})
	if !errors.Is(err, ErrStorageNotConfigured) {
		t.Fatalf("expected ErrStorageNotConfigured, got %v", err)
	}
}

func TestFileStoreDelegatesAvatarToStorage(t *testing.T) {
	storage := &fakeStorage{}
	mod, err := New(context.Background(), Options{Storage: storage})
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	tenantID := uuid.New()
	userID := uuid.New()
	stored, err := mod.Services.FileStore.StoreAvatar(context.Background(), StoreFileInput{
		TenantID:    tenantID,
		UserID:      userID,
		FileName:    "avatar.png",
		ContentType: "image/png",
		Content:     strings.NewReader("avatar"),
	})
	if err != nil {
		t.Fatalf("store avatar: %v", err)
	}
	if !storage.called {
		t.Fatal("storage was not called")
	}
	if storage.input.Purpose != FilePurposeUserAvatar {
		t.Fatalf("purpose = %q, want %q", storage.input.Purpose, FilePurposeUserAvatar)
	}
	if stored.ObjectID == uuid.Nil || stored.ObjectKey == "" {
		t.Fatalf("stored file missing object identity: %+v", stored)
	}
}

func TestEngageReplyDelegatesToMessaging(t *testing.T) {
	messaging := &fakeMessaging{}
	mod, err := New(context.Background(), Options{Messaging: messaging})
	if err != nil {
		t.Fatalf("new module: %v", err)
	}
	tenantID := uuid.New()
	threadID := uuid.New()
	receipt, err := mod.Services.EngageMessages.SendReply(context.Background(), EngageReplyInput{
		TenantID:  tenantID,
		ThreadID:  threadID,
		Channel:   ChannelWhatsApp,
		Recipient: "+15551230001",
		TextBody:  "Thanks, we will review this.",
	})
	if err != nil {
		t.Fatalf("send reply: %v", err)
	}
	if !messaging.called {
		t.Fatal("messaging was not called")
	}
	if messaging.tenantID != tenantID {
		t.Fatalf("tenant id = %s, want %s", messaging.tenantID, tenantID)
	}
	if messaging.req.Channel != ChannelWhatsApp {
		t.Fatalf("channel = %q, want %q", messaging.req.Channel, ChannelWhatsApp)
	}
	if messaging.req.Metadata["source"] != "engage" || messaging.req.Metadata["thread_id"] != threadID.String() {
		t.Fatalf("metadata missing engage context: %+v", messaging.req.Metadata)
	}
	if receipt.MessageID == uuid.Nil || !receipt.Accepted {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
}

type fakeStorage struct {
	called bool
	input  StoreFileInput
}

type fakeMessaging struct {
	called   bool
	tenantID uuid.UUID
	req      MessageRequest
}

func (f *fakeMessaging) Submit(_ context.Context, tenantID uuid.UUID, req MessageRequest) (*MessageReceipt, error) {
	f.called = true
	f.tenantID = tenantID
	f.req = req
	return &MessageReceipt{
		MessageID:      uuid.New(),
		Accepted:       true,
		Status:         "queued",
		IdempotencyKey: req.IdempotencyKey,
		CorrelationID:  req.CorrelationID,
		CreatedAt:      time.Now(),
	}, nil
}

func (f *fakeStorage) Store(_ context.Context, input StoreFileInput) (*StoredFile, error) {
	f.called = true
	f.input = input
	if input.Content != nil {
		_, _ = io.ReadAll(input.Content)
	}
	return &StoredFile{
		ObjectID:    uuid.New(),
		ObjectKey:   "tenants/example/users/avatar.png",
		Bucket:      "citual-dev",
		ContentType: input.ContentType,
		SizeBytes:   6,
	}, nil
}
