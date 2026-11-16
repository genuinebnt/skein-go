package config_test

import (
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/config"
)

func Example() {
	l := config.FromMap(map[string]string{
		"SKEIN_ENV":    "staging",
		"GRPC_ADDR":    ":9090",
		"DATABASE_URL": "postgres://identity:pw@db/skein",
	})
	svc := config.LoadService(l, "identity")
	addr := l.String("GRPC_ADDR", ":8080")
	timeout := l.Duration("REQUEST_TIMEOUT", 10*time.Second)
	dbURL := l.Secret("DATABASE_URL")
	if err := l.Err(); err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(svc.Name, svc.Env, addr, timeout, dbURL)
	// Output: identity staging :9090 10s [REDACTED]
}
