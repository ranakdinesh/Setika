package infrastructure

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ranakdinesh/setika/internal/config"
	"github.com/ranakdinesh/setika/internal/infrastructure/coredb"
	"github.com/ranakdinesh/setika/internal/infrastructure/db"
	"github.com/ranakdinesh/setika/internal/infrastructure/grpc"
	"github.com/ranakdinesh/setika/internal/infrastructure/hls"
	httpinfra "github.com/ranakdinesh/setika/internal/infrastructure/http"
	"github.com/ranakdinesh/setika/internal/infrastructure/messaging"
	"github.com/ranakdinesh/setika/internal/infrastructure/redis"
	"github.com/ranakdinesh/setika/internal/infrastructure/rtmp"
	"github.com/ranakdinesh/setika/internal/infrastructure/sse"
	"github.com/ranakdinesh/setika/internal/infrastructure/temporal"
	"github.com/ranakdinesh/setika/internal/infrastructure/websocket"
	"github.com/ranakdinesh/setika/internal/logger"
	goredis "github.com/redis/go-redis/v9"
)

type Infra struct {
	Config    *config.Config
	Log       *logger.Loggerx
	DB        *pgxpool.Pool
	Redis     *goredis.Client
	HTTP      *httpinfra.Server
	Temporal  *temporal.Client
	GRPC      *grpc.Server
	WS        *websocket.Hub
	SSE       *sse.Broker
	HLS       *hls.Server
	RTMP      *rtmp.Server
	Messaging *messaging.TolerantGateway
}

func Bootstrap(ctx context.Context, cfg *config.Config, log *logger.Loggerx) (*Infra, error) {
	// DB
	pool := db.NewPool(ctx, cfg.DatabaseURL)

	log.Info(ctx).Int32("max_conns", cfg.DBMaxConns).Msg("Database connected")
	if err := coredb.RunMigrations(ctx, pool); err != nil {
		return nil, fmt.Errorf("core db migrations: %w", err)
	}
	log.Info(ctx).Msg("Core database migrations ready")

	// Redis (optional)
	var rdb *goredis.Client
	if cfg.RedisEnabled {
		var err error
		rdb, err = redis.NewClient(cfg.RedisURL)
		if err != nil {
			return nil, fmt.Errorf("redis: %w", err)
		}
		log.Info(ctx).Msg("Redis connected")
	} else {
		log.Info(ctx).Msg("Redis disabled")
	}

	// HTTP (always on)
	httpServer := httpinfra.NewServer(httpinfra.Options{
		Addr:                  cfg.HTTPAddr,
		ReadTimeout:           cfg.ReadTimeout,
		WriteTimeout:          cfg.WriteTimeout,
		IdleTimeout:           cfg.IdleTimeout,
		MaxBodyBytes:          cfg.MaxBodyBytes,
		EnableCORS:            cfg.EnableCORS,
		AllowedOrigins:        cfg.CORSAllowedOrigins,
		AllowedMethods:        []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowedHeaders:        []string{"Accept", "Authorization", "Content-Type", "X-Request-Id"},
		OriginAllowed:         NewTenantOriginValidator(pool, cfg, log),
		EnableSecurityHeaders: cfg.EnableSecurityHeaders,
	}, log, nil)
	log.Info(ctx).Str("addr", cfg.HTTPAddr).Msg("HTTP server ready")

	// Temporal (optional)
	var tc *temporal.Client
	var err error
	if cfg.TemporalHost != "" {
		tc, err = temporal.New(cfg.TemporalHost)
		if err != nil {
			return nil, fmt.Errorf("temporal: %w", err)
		}
		log.Info(ctx).Str("host", cfg.TemporalHost).Msg("Temporal connected")
	} else {
		log.Info(ctx).Msg("Temporal disabled (TEMPORAL_HOST not set)")
	}

	infra := &Infra{
		Config:    cfg,
		Log:       log,
		DB:        pool,
		Redis:     rdb,
		HTTP:      httpServer,
		Temporal:  tc,
		Messaging: messaging.NewTolerantGateway(messagingLogger{log: log}),
	}

	// gRPC
	if cfg.GRPCEnabled {
		infra.GRPC = grpc.New(cfg.GRPCAddr, log)
		log.Info(ctx).Str("addr", cfg.GRPCAddr).Msg("gRPC server ready")
	} else {
		log.Info(ctx).Msg("gRPC disabled")
	}

	// WS
	if cfg.WSEnabled {
		infra.WS = websocket.NewHub(log)
		log.Info(ctx).Msg("WebSocket hub ready")
	} else {
		log.Info(ctx).Msg("WebSocket disabled")
	}

	// SSE
	if cfg.SSEEnabled {
		infra.SSE = sse.NewBroker(log)
		log.Info(ctx).Msg("SSE broker ready")
	} else {
		log.Info(ctx).Msg("SSE disabled")
	}

	// HLS
	if cfg.HLSEnabled {
		infra.HLS = hls.New(cfg.HLSAddr, cfg.HLSStoragePath, log)
		log.Info(ctx).Str("addr", cfg.HLSAddr).Msg("HLS server ready")
	} else {
		log.Info(ctx).Msg("HLS disabled")
	}

	// RTMP
	if cfg.RTMPEnabled {
		infra.RTMP = rtmp.New(cfg.RTMPAddr, log)
		log.Info(ctx).Str("addr", cfg.RTMPAddr).Msg("RTMP server ready")
	} else {
		log.Info(ctx).Msg("RTMP disabled")
	}

	return infra, nil
}
