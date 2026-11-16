package auth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/genuinebnt/skein-go/platform/auth"
)

func Example() {
	// identity: sign tokens and publish the public key set.
	key, _ := auth.GenerateKey()
	signer := auth.NewSigner(key, nil)
	jwks, _ := json.Marshal(auth.NewKeySet(signer.PublicKey()).JWKS())

	// A delegated token for an agent run acting as the user (ADR-0009).
	token, _, _ := signer.Issue(auth.Subject{UserID: "0193-user", AgentRunID: "0193-run"}, 10*time.Minute)

	// Any other service: verify with keys fetched from identity.
	keys := auth.NewRemoteKeySet(func(context.Context) ([]byte, error) { return jwks, nil }, auth.RemoteKeySetOptions{})
	verifier := auth.NewVerifier(keys, auth.VerifierOptions{})
	claims, err := verifier.Verify(context.Background(), token)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(claims.Subject.UserID, claims.Subject.AgentRunID, claims.Subject.IsAgent())
	// Output: 0193-user 0193-run true
}
