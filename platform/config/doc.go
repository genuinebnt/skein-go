// Package config reads a service's typed configuration from environment
// variables, applies defaults and reports every problem at once.
//
// # Design
//
// A Loader wraps a lookup function (os.LookupEnv in production, a map in
// tests). Each accessor (String, Int, Duration, Secret, ...) returns the
// parsed value or its default and records any error; Err returns all
// recorded errors joined, so a misconfigured deployment fails at startup
// with one message naming every bad variable instead of one per restart.
//
// Accessors are explicit calls rather than struct tags read by reflection.
// That keeps the code greppable (search for the variable name and you find
// the line that reads it), keeps validation next to the read, and ports
// directly to Rust (ADR-0012).
//
// Secret holds credentials. Its String, GoString, Format, LogValue and JSON
// forms are all "[REDACTED]", so a secret cannot leak through fmt, slog or
// an encoded config dump by accident; Reveal is the one deliberate way out.
//
// # Invariants
//
//   - A variable that is set but empty is treated as unset, so `FOO=` in an
//     env file falls back to the default instead of becoming "".
//   - Err is nil exactly when every accessor call succeeded.
//   - No accessor panics on bad input.
//
// # Porting notes
//
// Rust: a struct built by hand from `std::env::var`, with the errors
// collected into a Vec before returning; `secrecy::SecretString` replaces
// Secret.
package config
