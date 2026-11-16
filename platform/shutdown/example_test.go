package shutdown_test

import (
	"context"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/shutdown"
)

func Example() {
	m := shutdown.New(shutdown.Options{Timeout: 25 * time.Second})
	// Dependencies first: they stop last.
	m.Add("postgres", nil, func(context.Context) error { fmt.Println("pool closed"); return nil })
	m.Add("outbox-relay", func(ctx context.Context) error { <-ctx.Done(); fmt.Println("relay stopped"); return nil }, nil)
	m.Add("grpc", func(ctx context.Context) error { <-ctx.Done(); fmt.Println("server drained"); return nil }, nil)

	// In main this is m.Run(context.Background()) and SIGTERM ends it.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	fmt.Println("run:", m.Run(ctx))
	// Output:
	// server drained
	// relay stopped
	// pool closed
	// run: <nil>
}
