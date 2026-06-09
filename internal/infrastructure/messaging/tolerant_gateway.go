package messaging

import (
	"context"
	"errors"
	"sync"

	"github.com/google/uuid"
)

type Logger interface {
	WarnMessagingNotConfigured(ctx context.Context, operation string, channel Channel, err error)
}

type TolerantGateway struct {
	mu      sync.RWMutex
	gateway Gateway
	log     Logger
}

func NewTolerantGateway(log Logger) *TolerantGateway {
	return &TolerantGateway{log: log}
}

func (g *TolerantGateway) SetGateway(gateway Gateway) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gateway = gateway
}

func (g *TolerantGateway) Configured() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.gateway != nil
}

func (g *TolerantGateway) Submit(ctx context.Context, tenantID uuid.UUID, req Request) (*Receipt, error) {
	gateway := g.current()
	if gateway == nil {
		g.warn(ctx, "submit", req.Channel)
		return nil, ErrNotConfigured
	}
	return gateway.Submit(ctx, tenantID, req)
}

func (g *TolerantGateway) TrySubmit(ctx context.Context, tenantID uuid.UUID, req Request) (*Receipt, bool, error) {
	receipt, err := g.Submit(ctx, tenantID, req)
	if errors.Is(err, ErrNotConfigured) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	return receipt, true, nil
}

func (g *TolerantGateway) GetResult(ctx context.Context, tenantID, messageID uuid.UUID) (*Result, error) {
	gateway := g.current()
	if gateway == nil {
		g.warn(ctx, "get_result", "")
		return nil, ErrNotConfigured
	}
	return gateway.GetResult(ctx, tenantID, messageID)
}

func (g *TolerantGateway) current() Gateway {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.gateway
}

func (g *TolerantGateway) warn(ctx context.Context, operation string, channel Channel) {
	if g.log == nil {
		return
	}
	g.log.WarnMessagingNotConfigured(ctx, operation, channel, ErrNotConfigured)
}
