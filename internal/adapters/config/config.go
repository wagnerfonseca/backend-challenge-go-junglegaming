// Package config loads and validates the service configuration from the
// environment. It rejects integration failpoints outside integration binaries.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Environment names the configuration profile.
type Environment string

// Supported environments.
const (
	EnvironmentProduction  Environment = "production"
	EnvironmentIntegration Environment = "integration"
	EnvironmentDevelopment Environment = "development"
)

// HTTP holds the server and business request limits.
type HTTP struct {
	Addr             string
	ConcurrencyLimit int
	MaxBodyBytes     int64
}

// SQS holds the broker identity, queue URLs and consumer settings.
type SQS struct {
	Region          string
	Endpoint        string
	IngressQueueURL string
	EventQueueURL   string
	ConsumerName    string
}

// Database holds the PostgreSQL connection.
type Database struct {
	URL string
}

// OIDC holds the external identity provider contract.
type OIDC struct {
	IssuerURL string
	Audience  string
}

// Config is the complete service configuration.
type Config struct {
	Environment Environment
	ServiceName string
	InstanceID  string
	LogLevel    string
	Database    Database
	HTTP        HTTP
	SQS         SQS
	OIDC        OIDC
	// Providers maps an SQS SenderId to the provider it authenticates.
	Providers map[string]string
	// Failpoints lists the integration failpoints to enable. They are only
	// accepted by integration configuration.
	Failpoints []string
}

// Defaults used when the environment does not override them.
const (
	DefaultHTTPAddr             = ":8080"
	DefaultConcurrencyLimit     = 256
	DefaultMaxBodyBytes         = 1 << 20
	DefaultConsumerName         = "wager-transactions"
	DefaultIngressQueueName     = "wager-transactions.fifo"
	DefaultDLQName              = "wager-transactions-dlq.fifo"
	DefaultEventQueueName       = "wager-events.fifo"
	DefaultLogLevel             = "info"
	DefaultServiceName          = "distributed-wager-processing"
	DefaultSQSRegion            = "us-east-1"
	DefaultEndpoint             = "http://localhost:4566"
	DefaultProviderRegistration = "provider-a=provider-a"
	// DefaultReadinessTimeout bounds the PostgreSQL and SQS readiness probes.
	DefaultReadinessTimeout = 2 * time.Second
)

// Load reads the configuration from the process environment.
func Load() Config {
	cfg := Config{
		Environment: Environment(envOr("APP_ENV", string(EnvironmentProduction))),
		ServiceName: envOr("SERVICE_NAME", DefaultServiceName),
		InstanceID:  envOr("INSTANCE_ID", defaultInstanceID()),
		LogLevel:    envOr("LOG_LEVEL", DefaultLogLevel),
		Database: Database{
			URL: os.Getenv("DATABASE_URL"),
		},
		HTTP: HTTP{
			Addr:             envOr("HTTP_ADDR", DefaultHTTPAddr),
			ConcurrencyLimit: envIntOr("HTTP_CONCURRENCY_LIMIT", DefaultConcurrencyLimit),
			MaxBodyBytes:     int64(envIntOr("HTTP_MAX_BODY_BYTES", DefaultMaxBodyBytes)),
		},
		SQS: SQS{
			Region:          envOr("AWS_REGION", DefaultSQSRegion),
			Endpoint:        envOr("SQS_ENDPOINT", DefaultEndpoint),
			IngressQueueURL: envOr("SQS_INGRESS_QUEUE_URL", ""),
			EventQueueURL:   envOr("SQS_EVENT_QUEUE_URL", ""),
			ConsumerName:    envOr("SQS_CONSUMER_NAME", DefaultConsumerName),
		},
		OIDC: OIDC{
			IssuerURL: os.Getenv("OIDC_ISSUER_URL"),
			Audience:  os.Getenv("OIDC_AUDIENCE"),
		},
		Providers:  parseProviders(envOr("SQS_PROVIDER_SENDER_IDS", DefaultProviderRegistration)),
		Failpoints: parseList(os.Getenv("FAILPOINTS")),
	}
	return cfg
}

// Validate rejects configuration that cannot start the service. A failpoint
// outside an integration configuration is a production-safety violation.
func (c Config) Validate() error {
	if c.Database.URL == "" {
		return fmt.Errorf("config: DATABASE_URL is required")
	}
	if c.SQS.IngressQueueURL == "" {
		return fmt.Errorf("config: SQS_INGRESS_QUEUE_URL is required")
	}
	if c.SQS.EventQueueURL == "" {
		return fmt.Errorf("config: SQS_EVENT_QUEUE_URL is required")
	}
	if c.HTTP.ConcurrencyLimit <= 0 {
		return fmt.Errorf("config: HTTP_CONCURRENCY_LIMIT must be positive")
	}
	if c.HTTP.MaxBodyBytes <= 0 {
		return fmt.Errorf("config: HTTP_MAX_BODY_BYTES must be positive")
	}
	if len(c.Failpoints) > 0 && c.Environment != EnvironmentIntegration {
		return fmt.Errorf("config: failpoints %v are only allowed in integration configuration, not %q", c.Failpoints, c.Environment)
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("config: at least one SQS provider identity is required")
	}
	if c.OIDC.IssuerURL == "" {
		return fmt.Errorf("config: OIDC_ISSUER_URL is required")
	}
	if c.OIDC.Audience == "" {
		return fmt.Errorf("config: OIDC_AUDIENCE is required")
	}
	return nil
}

// FailpointsEnabled reports whether integration failpoints are active.
func (c Config) FailpointsEnabled() bool {
	return c.Environment == EnvironmentIntegration && len(c.Failpoints) > 0
}

// ProviderForSender maps an SQS SenderId to its configured provider identity.
func (c Config) ProviderForSender(senderID string) (string, bool) {
	provider, ok := c.Providers[senderID]
	return provider, ok
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envIntOr(key string, fallback int) int {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	var parsed int
	if _, err := fmt.Sscanf(value, "%d", &parsed); err != nil {
		return fallback
	}
	return parsed
}

func defaultInstanceID() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return "instance-unknown"
	}
	return host
}

func parseProviders(value string) map[string]string {
	providers := make(map[string]string)
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		sender, provider, found := strings.Cut(entry, "=")
		if !found || sender == "" || provider == "" {
			continue
		}
		providers[strings.TrimSpace(sender)] = strings.TrimSpace(provider)
	}
	return providers
}

func parseList(value string) []string {
	var values []string
	for _, entry := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(entry); trimmed != "" {
			values = append(values, trimmed)
		}
	}
	return values
}
