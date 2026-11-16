package grpc

import (
	"context"

	"github.com/genuinebnt/skein-go/platform/logging"
)

// ToStatus exposes toStatus to the external test package.
func ToStatus(err error) error { return toStatus(context.Background(), logging.Discard(), err) }
