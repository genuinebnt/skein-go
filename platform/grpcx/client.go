package grpcx

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
)

// ErrInvalidClientConfig means NewClient was given a config it cannot use.
var ErrInvalidClientConfig = errors.New("grpcx: invalid client config")

// RetryPolicy is the retry part of a gRPC service config. Zero fields take
// the defaults in DefaultRetryPolicy.
type RetryPolicy struct {
	MaxAttempts    int // including the first; gRPC caps this at 5
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Multiplier     float64
}

// DefaultRetryPolicy retries up to three more times, after about 100ms,
// 200ms and 400ms (each randomised by gRPC).
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 4, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second, Multiplier: 2}

// ClientConfig configures NewClient.
type ClientConfig struct {
	// Target is "dns:///host:port" for a headless Service. Other schemes
	// (passthrough, unix) work but defeat per-call load balancing.
	Target string
	// IdempotentMethods are retried on UNAVAILABLE. Each entry is a whole
	// service ("skein.v1.SearchService") or one method
	// ("skein.v1.IdentityService/Authorize"). Never list a method whose
	// repetition has an effect, such as one that appends or sends email.
	IdempotentMethods []string
	Retry             RetryPolicy
	// Insecure dials without TLS, for local runs and in-cluster traffic
	// without a mesh. Otherwise Credentials, or TLS with the system roots.
	Insecure    bool
	Credentials credentials.TransportCredentials
	// KeepaliveTime is the ping interval on idle connections (default 30s;
	// servers built by NewServer accept 10s or more).
	KeepaliveTime time.Duration

	TracerProvider trace.TracerProvider
	MeterProvider  metric.MeterProvider
	// Options are appended after the ones NewClient sets.
	Options []grpc.DialOption
}

// NewClient returns a connection to cfg.Target. Like grpc.NewClient it
// does not connect until the first call.
func NewClient(cfg ClientConfig) (*grpc.ClientConn, error) {
	if cfg.Target == "" {
		return nil, fmt.Errorf("%w: empty target", ErrInvalidClientConfig)
	}
	serviceConfig, err := ServiceConfigJSON(cfg.IdempotentMethods, cfg.Retry)
	if err != nil {
		return nil, err
	}
	creds := cfg.Credentials
	switch {
	case cfg.Insecure:
		creds = insecure.NewCredentials()
	case creds == nil:
		creds = credentials.NewTLS(nil)
	}
	keepaliveTime := cfg.KeepaliveTime
	if keepaliveTime == 0 {
		keepaliveTime = 30 * time.Second
	}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithDefaultServiceConfig(serviceConfig),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(otelOptions(cfg.TracerProvider, cfg.MeterProvider)...)),
		grpc.WithChainUnaryInterceptor(unaryClient),
		grpc.WithChainStreamInterceptor(streamClient),
		grpc.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                keepaliveTime,
			Timeout:             10 * time.Second,
			PermitWithoutStream: true,
		}),
	}
	conn, err := grpc.NewClient(cfg.Target, append(opts, cfg.Options...)...)
	if err != nil {
		return nil, fmt.Errorf("grpcx: dial %s: %w", cfg.Target, err)
	}
	return conn, nil
}

type methodName struct {
	Service string `json:"service"`
	Method  string `json:"method,omitempty"`
}

type retryPolicyJSON struct {
	MaxAttempts          int      `json:"maxAttempts"`
	InitialBackoff       string   `json:"initialBackoff"`
	MaxBackoff           string   `json:"maxBackoff"`
	BackoffMultiplier    float64  `json:"backoffMultiplier"`
	RetryableStatusCodes []string `json:"retryableStatusCodes"`
}

type methodConfig struct {
	Name        []methodName    `json:"name"`
	RetryPolicy retryPolicyJSON `json:"retryPolicy"`
}

type serviceConfigJSON struct {
	LoadBalancingConfig []map[string]struct{} `json:"loadBalancingConfig"`
	MethodConfig        []methodConfig        `json:"methodConfig,omitempty"`
}

// ServiceConfigJSON returns the gRPC service config NewClient uses:
// round_robin, plus a retry policy for the idempotent methods.
func ServiceConfigJSON(idempotent []string, policy RetryPolicy) (string, error) {
	cfg := serviceConfigJSON{LoadBalancingConfig: []map[string]struct{}{{"round_robin": {}}}}
	if len(idempotent) > 0 {
		names := make([]methodName, 0, len(idempotent))
		for _, entry := range idempotent {
			name, err := parseMethodName(entry)
			if err != nil {
				return "", err
			}
			names = append(names, name)
		}
		p := withRetryDefaults(policy)
		cfg.MethodConfig = []methodConfig{{
			Name: names,
			RetryPolicy: retryPolicyJSON{
				MaxAttempts:          p.MaxAttempts,
				InitialBackoff:       seconds(p.InitialBackoff),
				MaxBackoff:           seconds(p.MaxBackoff),
				BackoffMultiplier:    p.Multiplier,
				RetryableStatusCodes: []string{"UNAVAILABLE"},
			},
		}}
	}
	data, err := json.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("grpcx: service config: %w", err)
	}
	return string(data), nil
}

func parseMethodName(entry string) (methodName, error) {
	entry = strings.TrimPrefix(entry, "/")
	service, method, _ := strings.Cut(entry, "/")
	if service == "" || strings.Contains(method, "/") {
		return methodName{}, fmt.Errorf("%w: idempotent method %q is not \"pkg.Service\" or \"pkg.Service/Method\"", ErrInvalidClientConfig, entry)
	}
	return methodName{Service: service, Method: method}, nil
}

func withRetryDefaults(p RetryPolicy) RetryPolicy {
	d := DefaultRetryPolicy
	if p.MaxAttempts > 0 {
		d.MaxAttempts = p.MaxAttempts
	}
	if p.InitialBackoff > 0 {
		d.InitialBackoff = p.InitialBackoff
	}
	if p.MaxBackoff > 0 {
		d.MaxBackoff = p.MaxBackoff
	}
	if p.Multiplier > 0 {
		d.Multiplier = p.Multiplier
	}
	return d
}

// seconds formats a duration the way service config JSON expects ("0.1s").
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}
