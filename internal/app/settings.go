package app

import (
	"context"
	"fmt"
	"net/url"
	"strings"

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

	// Mirrors are the addresses this site is declared under, and Mirror is
	// the one in use. Most sites have one address and the choice is not
	// offered; MangaPark has fourteen, and they exist because these domains
	// are blocked and abandoned constantly.
	Mirrors []string
	Mirror  string
	// Address is an address the reader typed for the site, "" when they use
	// a declared one, and InUse the address atsume uses now.
	Address, InUse string

	// Series from this site, so the page that fixes a problem can name what
	// the problem was about and offer a way back to it.
	Series []store.Series
}

// ModuleSettings loads a module far enough to read its declarations.
//
// The options are declared in Init(), so the module has to be opened; there is
// no manifest to read them from.
func (a *App) ModuleSettings(ctx context.Context, name string) (*ModuleSettings, error) {
	// Everything stored is keyed by the site name, so a caller arriving with
	// a file name — an old series row, a typed URL — is resolved first rather
	// than quietly reading and writing settings under a second key.
	name = a.ResolveModule(ctx, name)
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
	if e, ok := a.SiteInfo(ctx, name); ok {
		if e.HasMirrors() {
			s.Mirrors = e.Mirrors
			s.Mirror = r.Module().RootURL
		}
		s.InUse = a.SiteAddress(ctx, e)
	}
	if addr, ok := CleanAddress(s.Values[AddressOption]); ok {
		s.Address = addr
	}
	if s.Series, err = a.Store.SeriesByModule(ctx, name); err != nil {
		return nil, err
	}
	return s, nil
}

// defaultText renders an option's declared default as text.
func defaultText(o scraper.Option) string {
	switch v := o.Default.(type) {
	case bool:
		if v {
			return "1"
		}
		return "0"
	case nil:
		return "0"
	default:
		return fmt.Sprint(v)
	}
}

// SaveModuleSettings stores option overrides and, optionally, a login.
//
// A blank username and password removes the stored credential rather than
// writing an empty one.
func (a *App) SaveModuleSettings(ctx context.Context, name string, options map[string]string, username, password string, updateLogin bool) error {
	name = a.ResolveModule(ctx, name)
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

// MirrorOption is the reserved setting holding which of a site's addresses to
// use. The double underscore keeps it clear of the option names modules
// declare, which are plain Lua identifiers.
const MirrorOption = "__mirror"

// AddressOption is the reserved setting holding an address the reader typed
// for a site, for one that moved somewhere its module does not declare.
const AddressOption = "__address"

// CleanAddress tidies a typed site address, reporting whether it is one:
// http or https, with a host, without a trailing slash.
func CleanAddress(s string) (string, bool) {
	s = strings.TrimRight(strings.TrimSpace(s), "/")
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", false
	}
	return s, true
}

// SiteAddress is the address atsume reads a site at: the one chosen or typed,
// otherwise the first it declares.
func (a *App) SiteAddress(ctx context.Context, e ModuleEntry) string {
	if m := a.mirrorFor(ctx, e); m != "" {
		return m
	}
	return e.Root()
}

// UseAddress points atsume at a different address for a site.
func (a *App) UseAddress(ctx context.Context, site, address string) error {
	addr, ok := CleanAddress(address)
	if !ok {
		return fmt.Errorf("%q is not a web address", address)
	}
	return a.Store.SetModuleOption(ctx, a.ResolveModule(ctx, site), AddressOption, addr)
}

// mirrorFor returns the address chosen for a site, or none to take the first.
//
// A choice that no longer appears among the declarations is ignored rather
// than honoured: these domains are abandoned constantly, and a stored address
// that upstream has dropped should not strand a followed series.
//
// An address the reader typed wins over the declared ones: it is how a site
// that moved to a domain upstream has not caught up with is still read.
func (a *App) mirrorFor(ctx context.Context, e ModuleEntry) string {
	opts, err := a.Store.ModuleOptions(ctx, e.Site)
	if err != nil {
		return ""
	}
	if addr, ok := CleanAddress(opts[AddressOption]); ok {
		return addr
	}
	if len(e.Mirrors) < 2 {
		return ""
	}
	chosen := opts[MirrorOption]
	for _, m := range e.Mirrors {
		if m == chosen {
			return chosen
		}
	}
	return ""
}

// openFileRaw opens one module file, selecting the website named by site.
//
// It is the one place that turns a file into a running module, and it applies
// nothing stored: reading a module's declarations must not depend on settings
// that a reader may be on their way to fix.
func (a *App) openFileRaw(ctx context.Context, file, site, rootURL string) (*scraper.Runner, error) {
	return a.Registry.HostWith(a.Limiter, a.Transport, a.Solver).Open(ctx, file, site, rootURL)
}

// openModuleRaw opens a site by name, without applying stored settings.
func (a *App) openModuleRaw(ctx context.Context, name string) (*scraper.Runner, error) {
	e, ok := a.SiteInfo(ctx, name)
	if !ok {
		return nil, errModuleNotFound(name, a.Registry.Ref())
	}
	return a.openFileRaw(ctx, e.File, e.Site, a.mirrorFor(ctx, e))
}

// TestLogin signs in to a site with the stored credentials and reports what
// happened.
//
// Typing a password and being told nothing is the worst feedback loop in the
// interface: the only way to find out used to be re-checking a series and
// seeing whether chapters appeared. The module already records a verdict —
// its account status — and nothing was reading it.
// The username and password are whatever is in the form, so a login can be
// tried before it is stored. Telling someone to save a password in order to
// find out whether it is right has the order backwards, and leaves a wrong
// one sitting in the database when it is not.
func (a *App) TestLogin(ctx context.Context, moduleKey, username, password string) (ok bool, detail string) {
	moduleKey = a.ResolveModule(ctx, moduleKey)
	// A blank field falls back to what is stored, so the button still tests
	// the saved login when nothing has been typed.
	if username == "" || password == "" {
		if !a.Sealer.Enabled() {
			return false, "No secret key is configured, so no login can be stored or tested."
		}
		creds, err := a.Store.Credentials(ctx, a.Sealer, moduleKey)
		if err != nil {
			return false, err.Error()
		}
		if creds == nil {
			return false, "Type a username and password to test them, or save them first."
		}
		if username == "" {
			username = creds.Username
		}
		if password == "" {
			password = creds.Password
		}
	}

	// Opened raw so a failing login cannot stop the page that fixes it from
	// loading; the credentials are applied by hand here instead.
	r, err := a.openModuleRaw(ctx, moduleKey)
	if err != nil {
		return false, err.Error()
	}
	defer r.Close()

	if !r.HasHandler("OnLogin") {
		return false, "This site does not take a login."
	}
	name := r.Module().Name
	r.SetAccount(username, password)

	// The module's own words are written for whoever wrote the module. What
	// the reader needs is which of the two things went wrong, because the
	// answers are different: fix the password, or stop trying.
	signedIn, err := r.Login()
	switch {
	case err != nil && strings.Contains(err.Error(), "rejected the credentials"):
		return false, name + " refused the sign-in. Check the username and password; " +
			"if they are right on the site itself, it may be asking for something " +
			"atsume cannot answer, such as a captcha or a second factor."
	case err != nil:
		return false, "The sign-in could not be completed: " + err.Error()
	case !signedIn:
		return false, name + " did not accept the sign-in, without saying why. " +
			"Signing in on the site itself will usually show what it wants."
	default:
		return true, "Signed in to " + name + " as " + username + "."
	}
}

// RecheckSite queues a fresh check of every series from one site.
//
// A setting only takes effect on the next check, which nothing said, so
// changing one appeared to do nothing at all.
func (a *App) RecheckSite(ctx context.Context, moduleKey string) (int, error) {
	series, err := a.Store.SeriesByModule(ctx, moduleKey)
	if err != nil {
		return 0, err
	}
	for _, v := range series {
		if err := a.EnqueueRefresh(ctx, v.Key(), v.URL); err != nil {
			return 0, err
		}
	}
	return len(series), nil
}

// UpdateModules fetches the latest commit of the modules' ref and switches
// to it. Downloads already running finish on the files they loaded.
func (a *App) UpdateModules(ctx context.Context) error {
	return a.Registry.Update(ctx)
}
