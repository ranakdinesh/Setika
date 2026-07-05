package hls

import (
	"context"

	"github.com/ranakdinesh/setika/internal/logger"
)

type Server struct {
	addr        string
	storagePath string
	log         *logger.Loggerx
}

func New(addr, storagePath string, log *logger.Loggerx) *Server {
	return &Server{
		addr:        addr,
		storagePath: storagePath,
		log:         log,
	}
}

func (s *Server) Start(ctx context.Context) error {
	s.log.Info(ctx).Str("addr", s.addr).Msg("HLS server starting")
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return nil
}
