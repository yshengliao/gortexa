// Package config builds Gortexa's configuration from layered sources with a
// fixed precedence: built-in defaults < YAML file < .env file < process
// environment (GORTEXA_ prefix, "__" for nesting). MustBuild fails loud:
// it panics on load errors, missing required values, or a JWT secret shorter
// than 32 bytes. Secret values mask themselves in logs and JSON.
package config

import (
	"fmt"
	"os"
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
	Addr            string        `koanf:"addr"`
	ShutdownTimeout time.Duration `koanf:"shutdown_timeout"`
	EnableCORS      bool          `koanf:"enable_cors"`
	CORSOrigins     []string      `koanf:"cors_origins"`
	OpenAPI         bool          `koanf:"openapi"`
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
	ServiceName string  `koanf:"service_name"`
	TracingOTLP string  `koanf:"tracing_otlp"` // OTLP endpoint; empty disables
	MetricsOTLP string  `koanf:"metrics_otlp"` // OTLP endpoint; empty disables
	SampleRatio float64 `koanf:"sample_ratio"`
}

func defaults() map[string]any {
	return map[string]any{
		"server.addr":             ":8080",
		"server.shutdown_timeout": "20s",
		"server.enable_cors":      false,
		"server.openapi":          true,
		"auth.issuer":             "gortexa",
		"auth.ttl":                "1h",
		"db.max_conns":            10,
		"cache.addr":              "localhost:6379",
		"cache.ttl":               "5m",
		"mq.driver":               "nats",
		"log.level":               "info",
		"log.format":              "json",
		"observ.service_name":     "gortexa",
		"observ.sample_ratio":     1.0,
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
			DecodeHook:       mapstructure.StringToTimeDurationHookFunc(),
		},
	}); err != nil {
		return nil, fmt.Errorf("unmarshal config: %w", err)
	}
	return &c, nil
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
	case len(c.Auth.JWTSecret) < 32:
		errs = append(errs, "auth.jwt_secret must be at least 32 bytes")
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
