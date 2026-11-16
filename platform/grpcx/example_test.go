package grpcx_test

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
	"github.com/genuinebnt/skein-go/platform/config"
	"github.com/genuinebnt/skein-go/platform/grpcx"
	"github.com/genuinebnt/skein-go/platform/logging"
)

func Example() {
	key, _ := auth.GenerateKey()
	verifier := auth.NewVerifier(auth.NewKeySet(auth.NewSigner(key, nil).PublicKey()), auth.VerifierOptions{})

	srv := grpcx.NewServer(grpcx.ServerConfig{
		Env:           config.EnvDev,
		Logger:        logging.Discard(),
		Verifier:      verifier,
		PublicMethods: []string{"/skein.v1.IdentityService/SignIn"},
	})
	// identityv1.RegisterIdentityServiceServer(srv, handlers) goes here.

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println(err)
		return
	}
	ctx, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer stop()
	fmt.Println("serve:", srv.Serve(ctx, lis)) // returns after ctx ends and the drain completes

	// Another service's client for identity.
	conn, err := grpcx.NewClient(grpcx.ClientConfig{
		Target:            "dns:///identity:9090",
		Insecure:          true,
		IdempotentMethods: []string{"skein.v1.IdentityService/Authorize", "skein.v1.IdentityService/GetUser"},
	})
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = conn.Close() }()
	fmt.Println("client for", conn.Target())
	// Output:
	// serve: <nil>
	// client for dns:///identity:9090
}
