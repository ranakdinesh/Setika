package grpc

import (
	"context"
	"fmt"
	"net"

	"github.com/ranakdinesh/setika/internal/logger"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

type Server struct {
	addr string
	log  *logger.Loggerx
	srv  *grpc.Server
}

func New(addr string, log *logger.Loggerx) *Server {
	srv := grpc.NewServer()
	reflection.Register(srv)
	return &Server{
		addr: addr,
		log:  log,
		srv:  srv,
	}
}

func (s *Server) Start(ctx context.Context) error {
	lis, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", s.addr, err)
	}
	s.log.Info(ctx).Str("addr", s.addr).Msg("gRPC server starting")
	return s.srv.Serve(lis)
}

func (s *Server) Stop(ctx context.Context) error {
	s.log.Info(ctx).Msg("gRPC server stopping")
	s.srv.GracefulStop()
	return nil
}
