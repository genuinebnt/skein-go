package telemetry_test

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/genuinebnt/skein-go/platform/logging"
	"github.com/genuinebnt/skein-go/platform/telemetry"
)

func Example() {
	ctx := context.Background()
	// In a service, the OTEL_* variables come from the deployment; here the
	// SDK is switched off so the example runs without a collector.
	tel, err := telemetry.Setup(ctx, telemetry.Config{
		Service: "identity", Version: "1.0.0", Env: "dev",
		Lookup: func(k string) (string, bool) {
			if k == "OTEL_SDK_DISABLED" {
				return "true", true
			}
			return os.LookupEnv(k)
		},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tel.Shutdown(shutdownCtx)
	}()

	log := logging.New(logging.Options{
		Service: "identity", Version: "1.0.0", Env: "dev",
		Extra: []slog.Handler{tel.LogHandler()}, // nil when logs export is off
	})
	_ = log
	fmt.Println("telemetry enabled:", tel.Enabled)
	// Output: telemetry enabled: false
}
