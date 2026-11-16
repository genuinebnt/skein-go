package config

// Env is the deployment environment. It switches behaviour that must differ
// between a laptop and production, such as gRPC reflection.
type Env string

// The environments Skein runs in (ADR-0014).
const (
	EnvDev     Env = "dev"
	EnvTest    Env = "test"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// IsDev reports whether developer conveniences (reflection, verbose errors)
// may be enabled.
func (e Env) IsDev() bool { return e == EnvDev || e == EnvTest }

// Service identifies a running service. Logs, traces and metrics carry all
// three fields.
type Service struct {
	// Name is fixed in code ("identity", "gateway"), not configured, so a
	// typo in a manifest cannot make one service report as another.
	Name string
	// Version is the build version, from SKEIN_VERSION (set by the image
	// build); "dev" when unset.
	Version string
	// Env is from SKEIN_ENV; "dev" when unset.
	Env Env
}

// LoadService reads SKEIN_VERSION and SKEIN_ENV for the service called name.
func LoadService(l *Loader, name string) Service {
	env := l.OneOf("SKEIN_ENV", string(EnvDev),
		string(EnvDev), string(EnvTest), string(EnvStaging), string(EnvProd))
	return Service{
		Name:    name,
		Version: l.String("SKEIN_VERSION", "dev"),
		Env:     Env(env),
	}
}
