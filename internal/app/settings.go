package app

import (
	"context"

	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
)

// ModuleSettings is everything the interface needs to render a module's
// configuration: what it declares, what the operator has overridden, and
// whether it takes a login.
type ModuleSettings struct {
	Module  string
	Options []scraper.Option
	// Values holds the current value of each option as text, the default when
	// the operator has not overridden it.
	Values map[string]string

	// SupportsLogin reports whether the module implements OnLogin.
	SupportsLogin bool
	// HasCredentials reports whether a login is stored, without decrypting it.
	HasCredentials bool
	Username       string
	// SecretsEnabled is false when no key is configured, in which case storing
	// a credential is refused rather than done in the clear.
	SecretsEnabled bool
}

// ModuleSettings loads a module far enough to read its declarations.
//
// The options are declared in Init(), so the module has to be opened; there is
// no manifest to read them from.
func (a *App) ModuleSettings(ctx context.Context, name string) (*ModuleSettings, error) {
	r, err := a.openModuleRaw(ctx, name)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	s := &ModuleSettings{
		Module:         r.Module().Name,
		Options:        r.Options(),
		Values:         map[string]string{},
		SupportsLogin:  r.HasHandler("OnLogin"),
		HasCredentials: a.Store.HasCredentials(ctx, name),
		SecretsEnabled: a.Sealer.Enabled(),
	}

	for _, o := range s.Options {
		s.Values[o.Name] = defaultText(o)
	}
	stored, err := a.Store.ModuleOptions(ctx, name)
	if err != nil {
		return nil, err
	}
	for k, v := range stored {
		s.Values[k] = v
	}

	if s.HasCredentials {
		if creds, err := a.Store.Credentials(ctx, a.Sealer, name); err == nil && creds != nil {
			s.Username = creds.Username
		}
	}
	return s, nil
}

// defaultText renders an option's declared default as text.
func defaultText(o scraper.Option) string {
	if o.Default == nil {
		return ""
	}
	switch o.Default.String() {
	case "true":
		return "1"
	case "false", "nil":
		return "0"
	default:
		return o.Default.String()
	}
}

// SaveModuleSettings stores option overrides and, optionally, a login.
//
// A blank username and password removes the stored credential rather than
// writing an empty one.
func (a *App) SaveModuleSettings(ctx context.Context, name string, options map[string]string, username, password string, updateLogin bool) error {
	for k, v := range options {
		if err := a.Store.SetModuleOption(ctx, name, k, v); err != nil {
			return err
		}
	}
	if !updateLogin {
		return nil
	}
	return a.Store.SetCredentials(ctx, a.Sealer, store.Credentials{
		ModuleName: name, Username: username, Password: password,
	})
}

// openModuleRaw opens a module without applying stored settings, which is what
// reading its declarations needs — applying a broken credential here would stop
// the operator reaching the page that lets them fix it.
func (a *App) openModuleRaw(ctx context.Context, name string) (*scraper.Runner, error) {
	info, ok := a.Registry.Find(a.ResolveModule(ctx, name))
	if !ok {
		return nil, errModuleNotFound(name, a.Registry.Ref())
	}
	return a.Registry.HostWith(a.Limiter, a.Transport, a.Solver).Open(ctx, info.File)
}
