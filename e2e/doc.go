// Package e2e holds Skein's end-to-end suite: user journeys driven through
// the gateway only (REST and WebSockets), against a running stack.
//
//	make up-native   # deploy/local: every service natively, small seed loaded
//	make e2e         # this suite (build tag e2e); RUN=TestSync narrows it
//	make down-native
//
// Every test file carries the e2e build tag, so `go test ./...` and
// `make test` never run it. The suite needs nothing but the stack's ports
// (deploy/local/ports.env, or E2E_GATEWAY_URL, E2E_IDENTITY_ADMIN_URL and
// E2E_MAILPIT_URL).
//
// # Design
//
//   - Requests and responses are the generated skeinv1 messages encoded with
//     protojson, exactly what grpc-gateway speaks, so the suite is checked
//     against the contracts by the compiler.
//   - Sync clients are real replicas: a libs/crdt Sequence per client, fed
//     by the sync socket in skein.v1.proto framing, so convergence is
//     checked on real CRDT state, not on strings the test wrote.
//   - Each test makes its own users (random addresses, passwords from
//     crypto/rand) and workspaces, so tests are independent and run in
//     parallel against a stack that already holds data.
//   - Seed journeys sign in as seed users through identity's dev-only
//     session route (IDENTITY_DEV_SESSIONS, admin port), because seed users
//     have no passwords. Everything else goes through the gateway.
//   - Asynchronous effects (indexing, projections, notifications, email)
//     are polled with a deadline; nothing sleeps for a fixed time.
package e2e
