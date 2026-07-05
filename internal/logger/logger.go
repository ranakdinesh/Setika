package logger

import (
	"context"
	//"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	//	"github.com/ranakdinesh/setika/internal/adapters/db"
	//	"github.com/ranakdinesh/setika/internal/adapters/db/sqlc"
	"github.com/rs/zerolog"
)

// ---------- Public API ----------

// Options configures the logger.
type Options struct {
	// Environment: "development", "staging", "testing", "production"
	Environment string

	// Database store for production logging
	//	Store db.Store

	// Buffer size for database sink (default 1024 log lines)
	Buffer int
}

type Loggerx struct {
	l zerolog.Logger
}

// DBSink implements io.Writer. It writes log lines to the database using sqlc.
// It is non-blocking: if buffer is full, it drops lines.
type DBSink struct {
	//store  db.Store
	ch     chan []byte
	buffer int
}

/*func NewDBSink(store db.Store, buffer int) *DBSink {
	s := &DBSink{
		store:  store,
		ch:     make(chan []byte, buffer),
		buffer: buffer,
	}
	go s.loop()
	return s
}

func (s *DBSink) Write(p []byte) (int, error) {
	// copy to avoid reuse of underlying slice by zerolog
	cp := make([]byte, len(p))
	copy(cp, p)

	select {
	case s.ch <- cp:
	default:
		// buffer full: drop silently (best effort)
	}
	return len(p), nil
}

func (s *DBSink) loop() {
	ctx := context.Background()
	for line := range s.ch {
		var logEntry map[string]interface{}
		if err := json.Unmarshal(line, &logEntry); err != nil {
			continue
		}

		// Extract fields
		timestampStr, _ := logEntry["ts"].(string)
		timestamp, _ := time.Parse(time.RFC3339, timestampStr)
		level, _ := logEntry["lvl"].(string)
		message, _ := logEntry["msg"].(string)
		caller, _ := logEntry["caller"].(string)
		traceID, _ := logEntry["trace_id"].(string)
		tenantID, _ := logEntry["tenant_id"].(string)
		userID, _ := logEntry["user_id"].(string)

		// Remove extracted fields to store the rest as properties
		delete(logEntry, "ts")
		delete(logEntry, "lvl")
		delete(logEntry, "msg")
		delete(logEntry, "caller")
		delete(logEntry, "trace_id")
		delete(logEntry, "tenant_id")
		delete(logEntry, "user_id")

		properties, _ := json.Marshal(logEntry)

		// Insert into database using sqlc
		err := s.store.CreateLog(ctx, sqlc.CreateLogParams{
			Timestamp:  timestamp,
			Level:      level,
			Message:    message,
			TraceID:    strPtr(traceID),
			TenantID:   strPtr(tenantID),
			UserID:     strPtr(userID),
			Caller:     strPtr(caller),
			Properties: properties,
		})

		if err != nil {
			// In case of DB error, maybe print to stderr as fallback
			fmt.Fprintf(os.Stderr, "Failed to write log to DB: %v\n", err)
		}
	}
}*/

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// NewWithOptions is the preferred constructor.
func NewWithOptions(opts Options) *Loggerx {
	// ---------- Global zerolog config ----------
	zerolog.SetGlobalLevel(parseLevel(getEnv("LOG_LEVEL", "info")))

	zerolog.TimeFieldFormat = time.RFC3339
	zerolog.TimestampFieldName = "ts"
	zerolog.LevelFieldName = "lvl"
	zerolog.MessageFieldName = "msg"
	zerolog.CallerFieldName = "caller"

	zerolog.CallerMarshalFunc = func(_ uintptr, file string, line int) string {
		parts := strings.Split(file, "/")
		if len(parts) > 2 {
			file = strings.Join(parts[len(parts)-2:], "/")
		}
		return fmt.Sprintf("%s:%d", file, line)
	}

	writers := make([]io.Writer, 0, 2)

	isProd := strings.ToLower(opts.Environment) == "production"

	if !isProd {
		// Development/Staging/Testing: Console output
		cw := zerolog.ConsoleWriter{
			Out:        os.Stdout,
			TimeFormat: "2006-01-02 15:04:05.000",
		}
		cw.FormatCaller = func(i interface{}) string {
			if caller, ok := i.(string); ok {
				parts := strings.Split(caller, "/")
				if len(parts) > 2 {
					return parts[len(parts)-2] + "/" + parts[len(parts)-1]
				}
				return caller
			}
			return fmt.Sprintf("%v", i)
		}
		writers = append(writers, cw)
	} else {
		// Production: Database sink
		/*	if opts.Store != nil {
			dbs := NewDBSink(opts.Store, firstNonZeroInt(opts.Buffer, 1024))
			writers = append(writers, dbs)
		} else {*/
		// Fallback to stdout if Store is missing in prod
		writers = append(writers, os.Stdout)
	}
	//}

	mw := io.MultiWriter(writers...)

	base := zerolog.New(mw).With().Caller().Timestamp().CallerWithSkipFrameCount(2).Logger()

	if !isProd {
		base.Debug().Str("env", opts.Environment).Msg("logger online")
	}

	return &Loggerx{l: base}
}

// New keeps backward compatibility with previous code paths.
func New(dev bool) *Loggerx {
	env := "production"
	if dev {
		env = "development"
	}
	return NewWithOptions(Options{Environment: env})
}

// With adds structured fields.
func (x *Loggerx) With(kv ...interface{}) *Loggerx {
	return &Loggerx{l: x.l.With().Fields(kv).Logger()}
}

// Accessors (context-aware)
func (x *Loggerx) Info(ctx context.Context) *zerolog.Event  { return bindCtx(x.l, ctx).Info() }
func (x *Loggerx) Error(ctx context.Context) *zerolog.Event { return bindCtx(x.l, ctx).Error() }
func (x *Loggerx) Warn(ctx context.Context) *zerolog.Event  { return bindCtx(x.l, ctx).Warn() }
func (x *Loggerx) Debug(ctx context.Context) *zerolog.Event { return bindCtx(x.l, ctx).Debug() }
func (x *Loggerx) Fatal(ctx context.Context) *zerolog.Event { return bindCtx(x.l, ctx).Fatal() }
func (x *Loggerx) Panic(ctx context.Context) *zerolog.Event { return bindCtx(x.l, ctx).Panic() }

// Logger returns the underlying zerolog (advanced usage).
func (x *Loggerx) Logger() zerolog.Logger { return x.l }

// ---------- Context helpers ----------

type ctxKey string

const (
	ctxKeyTraceID  ctxKey = "trace_id"
	ctxKeyTenantID ctxKey = "tenant_id"
	ctxKeyUserID   ctxKey = "user_id"
)

func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, ctxKeyTraceID, traceID)
}
func WithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKeyTenantID, tenantID)
}
func WithUserID(ctx context.Context, userID string) context.Context {
	return context.WithValue(ctx, ctxKeyUserID, userID)
}

func TraceIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyTraceID).(string)
	return v, ok
}
func TenantIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyTenantID).(string)
	return v, ok
}
func UserIDFrom(ctx context.Context) (string, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(string)
	return v, ok
}

// ---------- Internal ----------

func bindCtx(l zerolog.Logger, ctx context.Context) *zerolog.Logger {
	if ctx == nil {
		return &l
	}
	ev := l.With()
	if v, ok := TraceIDFrom(ctx); ok && v != "" {
		ev = ev.Str("trace_id", v)
	}
	if v, ok := TenantIDFrom(ctx); ok && v != "" {
		ev = ev.Str("tenant_id", v)
	}
	if v, ok := UserIDFrom(ctx); ok && v != "" {
		ev = ev.Str("user_id", v)
	}
	ll := ev.Logger()
	return &ll
}

func firstNonZeroInt(v, d int) int {
	if v == 0 {
		return d
	}
	return v
}

func getEnv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func parseLevel(s string) zerolog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "trace":
		return zerolog.TraceLevel
	case "debug":
		return zerolog.DebugLevel
	case "info", "":
		return zerolog.InfoLevel
	case "warn", "warning":
		return zerolog.WarnLevel
	case "error", "err":
		return zerolog.ErrorLevel
	case "fatal":
		return zerolog.FatalLevel
	case "panic":
		return zerolog.PanicLevel
	case "disabled", "off", "none":
		return zerolog.Disabled
	default:
		return zerolog.InfoLevel
	}
}
