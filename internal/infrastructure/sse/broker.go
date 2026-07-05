package sse

import (
	"github.com/ranakdinesh/setika/internal/logger"
)

type Broker struct {
	log *logger.Loggerx
}

func NewBroker(log *logger.Loggerx) *Broker {
	b := &Broker{
		log: log,
	}
	b.log.Info(nil).Msg("SSE broker started")
	return b
}

func (b *Broker) Subscribe(clientID string) chan []byte {
	return make(chan []byte) // Stub
}

func (b *Broker) Unsubscribe(clientID string, ch chan []byte) {
	// Stub
}

func (b *Broker) Publish(clientID string, data []byte) {
	// Stub
}
