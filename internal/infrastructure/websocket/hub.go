package websocket

import (
	"github.com/ranakdinesh/setika/internal/logger"
)

type Hub struct {
	log *logger.Loggerx
}

func NewHub(log *logger.Loggerx) *Hub {
	return &Hub{
		log: log,
	}
}

func (h *Hub) Run() {
	h.log.Info(nil).Msg("WebSocket hub started")
}

func (h *Hub) BroadcastTenant(tenantID string, msg []byte) {
	// Stub
}

func (h *Hub) BroadcastUser(userID string, msg []byte) {
	// Stub
}

func (h *Hub) BroadcastChannel(channel string, msg []byte) {
	// Stub
}
