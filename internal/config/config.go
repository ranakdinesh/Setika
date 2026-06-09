package config

import (
	"bufio"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv                string        `env:"APP_ENV" default:"development"`
	OtelServiceName       string        `env:"OTEL_SERVICE_NAME" default:"oauth-service"`
	OtelExporterEndpoint  string        `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	HTTPAddr              string        `env:"HTTP_ADDR" default:":8080"`
	GRPCAddr              string        `env:"GRPC_ADDR" default:":9090"`
	ReadTimeout           time.Duration `env:"HTTP_READ_TIMEOUT" default:"15s"`
	WriteTimeout          time.Duration `env:"HTTP_WRITE_TIMEOUT" default:"30s"`
	IdleTimeout           time.Duration `env:"HTTP_IDLE_TIMEOUT" default:"60s"`
	MaxBodyBytes          int64         `env:"HTTP_MAX_BODY_BYTES" default:"10485760"`
	EnableCORS            bool          `env:"HTTP_ENABLE_CORS" default:"true"`
	EnableSecurityHeaders bool          `env:"HTTP_ENABLE_SECURITY_HEADERS" default:"true"`
	CORSAllowedOrigins    []string      `env:"CORS_ALLOWED_ORIGINS" default:"*" split:","`
	DatabaseURL           string        `env:"DATABASE_URL"`
	OAuthIssuer           string        `env:"OAUTH_ISSUER"`
	OAuthAudience         string        `env:"OAUTH_AUDIENCE"`
	OAuthJWKSURL          string        `env:"OAUTH_JWKS_URL"`
	APIKeyHeader          string        `env:"API_KEY_HEADER"`
	APIKeyValue           string        `env:"API_KEY_VALUE"`
	LogServiceURL         string        `env:"LOG_SVC_URL"`
	LogServiceKey         string        `env:"LOG_SVC_API_KEY"`
	JWTPrivateKeyPath     string        `env:"JWT_PRIVATE_KEY_PATH"`
	TenantHeader          string        `env:"TENANT_HEADER"`
	FositeGlobalSecret    string        `env:"FOSITE_GLOBAL_SECRET"`
	AuthClientID          string        `env:"AUTH_CLIENT_ID" envDefault:""`
	AuthClientSecret      string        `env:"AUTH_CLIENT_SECRET" envDefault:""`
	AppName               string        `env:"APP_NAME" default:"spur"`
	LogLevel              string        `env:"LOG_LEVEL" default:"info"`
	FrontendURL           string        `env:"FRONTEND_URL" default:"http://localhost:3000"`
	GRPCEnabled           bool          `env:"GRPC_ENABLED"    default:"false"`
	WSEnabled             bool          `env:"WS_ENABLED"      default:"false"`
	SSEEnabled            bool          `env:"SSE_ENABLED"     default:"false"`
	HLSEnabled            bool          `env:"HLS_ENABLED"     default:"false"`
	HLSAddr               string        `env:"HLS_ADDR"        default:":8888"`
	HLSStoragePath        string        `env:"HLS_STORAGE_PATH" default:"./data/hls"`
	RTMPEnabled           bool          `env:"RTMP_ENABLED"    default:"false"`
	RTMPAddr              string        `env:"RTMP_ADDR"       default:":1935"`
	RedisEnabled          bool          `env:"REDIS_ENABLED"   default:"false"`
	RedisURL              string        `env:"REDIS_URL"       default:"redis://localhost:6379"`
	DBMaxConns            int32         `env:"DB_MAX_CONNS"    default:"20"`
	TemporalHost          string        `env:"TEMPORAL_HOST"`
	TemporalNamespace     string        `env:"TEMPORAL_NAMESPACE" default:"default"`

	IdentityIssuer string `env:"IDENTITY_ISSUER"      envDefault:"http://localhost:8080"`
}

func Load(out any) error {
	// Try to load .env file from common locations (useful when running from cmd/ subdirectories)
	envPaths := []string{".env", "../.env", "../../.env"}
	for _, path := range envPaths {
		err := loadEnvFile(path)
		if err == nil {
			break // Successfully loaded
		}
	}

	v := reflect.ValueOf(out)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return errors.New("config: out must be pointer to struct")
	}
	v = v.Elem()
	t := v.Type()

	var errs []string

	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" { // unexported
			continue
		}
		// Nested struct support (one level)
		if f.Type.Kind() == reflect.Struct && f.Anonymous == false {
			subPtr := v.Field(i).Addr().Interface()
			if err := Load(subPtr); err != nil {
				errs = append(errs, fmt.Sprintf("%s: %v", f.Name, err))
			}
			continue
		}

		envName := f.Tag.Get("env")
		if envName == "" {
			// default to field name in upper snake: AppPort -> APP_PORT
			envName = toEnvName(f.Name)
		}
		raw, ok := os.LookupEnv(envName)
		if !ok {
			if def := f.Tag.Get("default"); def != "" {
				raw = def
				ok = true
			}
		}
		req := f.Tag.Get("required") == "true"

		if !ok {
			if req {
				errs = append(errs, fmt.Sprintf("missing required %q", envName))
			}
			continue
		}

		if err := setField(v.Field(i), raw, f.Tag.Get("split")); err != nil {
			errs = append(errs, fmt.Sprintf("%s (%s): %v", f.Name, envName, err))
		}
	}

	if len(errs) > 0 {
		return errors.New("config: \n - " + strings.Join(errs, "\n - "))
	}
	return nil
}

// MustLoad is a convenience wrapper that panics on error.
func MustLoad(out any) {
	if err := Load(out); err != nil {
		panic(err)
	}
}

func loadEnvFile(filename string) error {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		// Remove quotes if present
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}

		// Only set if not already set in environment
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
	return scanner.Err()
}

func setField(fv reflect.Value, raw string, split string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid bool %q", raw)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// Special-case time.Duration
		if fv.Type() == reflect.TypeOf(time.Duration(0)) {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return fmt.Errorf("invalid duration %q", raw)
			}
			fv.SetInt(int64(d))
			return nil
		}
		i, err := strconv.ParseInt(raw, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("invalid int %q", raw)
		}
		fv.SetInt(i)
	case reflect.Slice:
		if fv.Type().Elem().Kind() != reflect.String {
			return fmt.Errorf("unsupported slice element type %s", fv.Type().Elem().Kind())
		}
		sep := ","
		if split != "" {
			sep = split
		}
		parts := strings.Split(raw, sep)
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		fv.Set(reflect.ValueOf(out))
	default:
		return fmt.Errorf("unsupported kind %s", fv.Kind())
	}
	return nil
}

func toEnvName(field string) string {
	var b strings.Builder
	for i, r := range field {
		if i > 0 && isUpper(r) && (i+1 < len(field) && !isUpper(rune(field[i+1]))) {
			b.WriteByte('_')
		}
		b.WriteRune(toUpper(r))
	}
	return b.String()
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }
func toUpper(r rune) rune {
	if r >= 'a' && r <= 'z' {
		return r - 'a' + 'A'
	}
	return r
}
func LoadPrivateKey(path string) (*rsa.PrivateKey, error) {
	if path == "" {
		return nil, errors.New("config: private key path is empty")
	}

	keyBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config: failed to read private key file: %w", err)
	}

	block, _ := pem.Decode(keyBytes)
	if block == nil {
		return nil, errors.New("config: failed to decode PEM block containing private key")
	}

	// Try parsing as PKCS1 (traditional RSA format)
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}

	// Try parsing as PKCS8 (newer format)
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
		return nil, errors.New("config: key is not an RSA private key")
	}

	return nil, errors.New("config: failed to parse private key (must be PKCS1 or PKCS8 RSA)")
}
func LoadRSAKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read key %s: %w", path, err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in %s", path)
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse key: %w", err)
	}
	return key, nil
}
