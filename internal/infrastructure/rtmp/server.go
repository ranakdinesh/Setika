package rtmp

import (
	"context"

	"github.com/ranakdinesh/setika/internal/logger"
)

type Server struct {
	addr string
	log  *logger.Loggerx
}

func New(addr string, log *logger.Loggerx) *Server {
	return &Server{
		addr: addr,
		log:  log,
	}
}

func (s *Server) Start(ctx context.Context) error {
	s.log.Info(ctx).Str("addr", s.addr).Msg("RTMP server starting")
	return nil
}

func (s *Server) Stop(ctx context.Context) error {
	return nil
}
