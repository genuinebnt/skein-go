package logging_test

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/genuinebnt/skein-go/platform/logging"
)

func Example() {
	log := logging.New(logging.Options{
		Service: "identity", Version: "1.0.0", Env: "dev",
		Writer: os.Stdout,
	})

	ctx := logging.WithRequestID(context.Background(), "req-42")
	ctx = logging.WithSubject(ctx, "0193a1b2-user", "")

	// A zero time is omitted by the JSON handler, which keeps the example
	// output stable; real callers use log.InfoContext.
	r := slog.NewRecord(time.Time{}, slog.LevelInfo, "signed in", 0)
	r.AddAttrs(slog.String("password", "hunter2"), slog.Int("attempts", 1))
	_ = log.Handler().Handle(ctx, r)
	// Output: {"level":"INFO","msg":"signed in","service":"identity","version":"1.0.0","env":"dev","request_id":"req-42","user_id":"0193a1b2-user","password":"[REDACTED]","attempts":1}
}
