// Package config builds Gortexa's configuration from layered sources with a
// fixed precedence: built-in defaults < YAML file < .env file < process
// environment (GORTEXA_ prefix, "__" for nesting). MustBuild fails loud:
// it panics on load errors, missing required values, or a JWT secret shorter
// than 32 bytes. Secret values mask themselves in logs and JSON.
package config

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/parsers/dotenv"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	env "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

// devPlaceholderSecret is the sample JWT secret shipped in etc/config.yaml and
// cloned into every `gortexa create` project. Validate rejects it so a real
// deployment can never accidentally run on the publicly-known key.
const devPlaceholderSecret = "dev-only-insecure-secret-change-me-please"

// Config is the root configuration.
type Config struct {
	Server ServerConfig `koanf:"server"`
	Auth   AuthConfig   `koanf:"auth"`
	DB     DBConfig     `koanf:"db"`
	Cache  CacheConfig  `koanf:"cache"`
	MQ     MQConfig     `koanf:"mq"`
	Log    LogConfig    `koanf:"log"`
	Observ ObservConfig `koanf:"observ"`
}

type ServerConfig struct {
	Addr              string        `koanf:"addr"`
	ShutdownTimeout   time.Duration `koanf:"shutdown_timeout"`
	ReadTimeout       time.Duration `koanf:"read_timeout"`
	WriteTimeout      time.Duration `koanf:"write_timeout"`
	IdleTimeout       time.Duration `koanf:"idle_timeout"`
	ReadHeaderTimeout time.Duration `koanf:"read_header_timeout"`
	EnableCORS        bool          `koanf:"enable_cors"`
	CORSOrigins       []string      `koanf:"cors_origins"`
	OpenAPI           bool          `koanf:"openapi"`
}

type AuthConfig struct {
	JWTSecret Secret        `koanf:"jwt_secret"`
	Issuer    string        `koanf:"issuer"`
	TTL       time.Duration `koanf:"ttl"`
}

type DBConfig struct {
	DSN      Secret `koanf:"dsn"`
	MaxConns int32  `koanf:"max_conns"`
}

type CacheConfig struct {
	Addr     string        `koanf:"addr"`
	Password Secret        `koanf:"password"`
	DB       int           `koanf:"db"`
	TTL      time.Duration `koanf:"ttl"`
}

type MQConfig struct {
	Driver string   `koanf:"driver"` // "nats" | "kafka"
	URL    string   `koanf:"url"`
	Topics []string `koanf:"topics"`
}

type LogConfig struct {
	Level  string `koanf:"level"`  // debug|info|warn|error
	Format string `koanf:"format"` // json|text
}

type ObservConfig struct {
	ServiceName         string   `koanf:"service_name"`
	ServiceVersion      string   `koanf:"service_version"`
	TracingOTLP         string   `koanf:"tracing_otlp"` // OTLP endpoint; empty disables
	MetricsOTLP         string   `koanf:"metrics_otlp"` // OTLP endpoint; empty disables
	LogsOTLP            string   `koanf:"logs_otlp"`
	SampleRatio         float64  `koanf:"sample_ratio"`
	GenAICaptureContent bool     `koanf:"genai_capture_content"`
	GenAIMaskFields     []string `koanf:"genai_mask_fields"`
	SLOErrorBudget      float64  `koanf:"slo_error_budget"`
}

func defaults() map[string]any {
	return map[string]any{
		"server.addr":             ":8080",
		"server.shutdown_timeout": "20s",
		"server.read_timeout":     "15s",
		// 0 = disabled: the single h2c server multiplexes long-lived gRPC
		// server-streams (Health.Watch) and MCP SSE, which a per-stream
		// WriteTimeout would kill mid-flight.
		"server.write_timeout":       "0",
		"server.idle_timeout":        "60s",
		"server.read_header_timeout": "5s",
		"server.enable_cors":         false,
		"server.openapi":             true,
		"auth.issuer":                "gortexa",
		"auth.ttl":                   "1h",
		"db.max_conns":               10,
		"cache.addr":                 "localhost:6379",
		"cache.ttl":                  "5m",
		"mq.driver":                  "nats",
		"log.level":                  "info",
		"log.format":                 "json",
		"observ.service_name":        "gortexa",
		"observ.service_version":     "dev",
		"observ.sample_ratio":        1.0,
		"observ.genai_mask_fields":   []string{"password", "token", "secret", "authorization", "api_key"},
		"observ.slo_error_budget":    0.001,
	}
}

// Option configures Build.
type Option func(*options)

type options struct {
	configFile string
	dotenvFile string
	prefix     string
	environ    func() []string
}

// WithConfigFile loads a YAML file layer.
func WithConfigFile(path string) Option { return func(o *options) { o.configFile = path } }

// WithDotEnvFile loads a .env file layer (keys use the env prefix/format).
func WithDotEnvFile(path string) Option { return func(o *options) { o.dotenvFile = path } }

// WithEnvPrefix overrides the environment prefix (default "GORTEXA_").
func WithEnvPrefix(prefix string) Option { return func(o *options) { o.prefix = prefix } }

// WithEnviron injects the environment (default os.Environ) — used in tests.
func WithEnviron(fn func() []string) Option { return func(o *options) { o.environ = fn } }

func envKeyToPath(prefix string) func(string) string {
	return func(s string) string {
		s = strings.TrimPrefix(s, prefix)
		s = strings.ToLower(s)
		return strings.ReplaceAll(s, "__", ".")
	}
}

// Build assembles a Config from all configured layers (without validation).
func Build(opts ...Option) (*Config, error) {
	o := options{prefix: "GORTEXA_", environ: os.Environ}
	for _, opt := range opts {
		opt(&o)
	}

	k := koanf.New(".")
	if err := k.Load(confmap.Provider(defaults(), "."), nil); err != nil {
		return nil, fmt.Errorf("load defaults: %w", err)
	}
	if o.configFile != "" {
		if err := k.Load(file.Provider(o.configFile), yaml.Parser()); err != nil {
			return nil, fmt.Errorf("load config file %q: %w", o.configFile, err)
		}
	}
	if o.dotenvFile != "" {
		if err := k.Load(file.Provider(o.dotenvFile), dotenv.ParserEnv(o.prefix, ".", envKeyToPath(o.prefix))); err != nil {
			return nil, fmt.Errorf("load dotenv %q: %w", o.dotenvFile, err)
		}
	}
	transform := envKeyToPath(o.prefix)
	if err := k.Load(env.Provider(".", env.Opt{
		Prefix:        o.prefix,
		EnvironFunc:   o.environ,
		TransformFunc: func(key, val string) (string, any) { return transform(key), val },
	}), nil); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	var c Config
	if err := k.UnmarshalWithConf("", &c, koanf.UnmarshalConf{
		Tag: "koanf",
		DecoderConfig: &mapstructure.DecoderConfig{
			Result:           &c,
			TagName:          "koanf",
			WeaklyTypedInput: true,
			DecodeHook: mapstructure.ComposeDecodeHookFunc(
				mapstructure.StringToTimeDurationHookFunc(),
				// Env/dotenv values arrive as strings; split comma lists into slices
				// so a list-valued key (cors_origins, mq.topics) can be set from the
				// environment instead of collapsing to a single-element slice.
				mapstructure.StringToSliceHookFunc(","),
				rejectBareNumericDuration(),
			),
		},
	}); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &c, nil
}

// rejectBareNumericDuration fails a config load when a time.Duration field is
// given a bare number (e.g. `shutdown_timeout: 30`), which mapstructure's weak
// typing would otherwise silently read as 30 nanoseconds. Durations must be
// strings like "30s".
func rejectBareNumericDuration() mapstructure.DecodeHookFuncType {
	durationType := reflect.TypeOf(time.Duration(0))
	return func(from reflect.Type, to reflect.Type, data any) (any, error) {
		// Skip when the target isn't a Duration, or when the value is already a
		// Duration (e.g. the prior string→Duration hook already converted "30s";
		// time.Duration's own kind is Int64, which we must not reject).
		if to != durationType || from == durationType {
			return data, nil
		}
		switch from.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			return nil, fmt.Errorf("duration must be a string like \"30s\", not the bare number %v (a bare number would be read as nanoseconds)", data)
		}
		return data, nil
	}
}

// Validate enforces required fields and invariants.
func (c *Config) Validate() error {
	var errs []string
	if c.Server.Addr == "" {
		errs = append(errs, "server.addr is required")
	}
	switch {
	case c.Auth.JWTSecret == "":
		errs = append(errs, "auth.jwt_secret is required")
	case c.Auth.JWTSecret.Reveal() == devPlaceholderSecret:
		errs = append(errs, "auth.jwt_secret is the built-in dev placeholder; set a real secret via GORTEXA_AUTH__JWT_SECRET")
	case len(c.Auth.JWTSecret) < 32:
		errs = append(errs, "auth.jwt_secret must be at least 32 bytes")
	}
	// Require a non-empty issuer so the JWT issuer check is always active. An
	// empty issuer silently disables jwt.WithIssuer (see internal/auth), which
	// would accept any token minted with a shared secret regardless of its iss
	// claim — defence-in-depth an operator should not be able to drop by
	// blanking GORTEXA_AUTH__ISSUER.
	if strings.TrimSpace(c.Auth.Issuer) == "" {
		errs = append(errs, "auth.issuer is required")
	}
	if len(errs) > 0 {
		return fmt.Errorf("invalid config: %s", strings.Join(errs, "; "))
	}
	return nil
}

// MustBuild builds and validates, panicking on any failure (fail-loud startup).
func MustBuild(opts ...Option) *Config {
	c, err := Build(opts...)
	if err != nil {
		panic("config: " + err.Error())
	}
	if err := c.Validate(); err != nil {
		panic("config: " + err.Error())
	}
	return c
}
