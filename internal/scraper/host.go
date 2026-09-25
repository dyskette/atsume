package scraper

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	rt "github.com/arnodel/golua/runtime"
)

// Host loads website modules from a checkout of the FMD2 lua tree.
//
// The tree is fetched at runtime and never vendored: FMD2's modules are
// GPL-2.0-only, which is incompatible with this project's GPL-3.0 licence, so
// they must not become part of this work. See docs/MODULES.md.
type Host struct {
	// LuaDir is the `lua` directory of an FMD2 checkout.
	LuaDir string
	// Limiter gates outbound requests across all runners.
	Limiter Limiter
	// Transport, when set, replaces the default HTTP transport. Tests supply a
	// Cassette here to replay recorded pages.
	Transport http.RoundTripper
	// Solver, when set, recovers requests refused by an anti-bot interstitial.
	Solver *Flaresolverr
}

// Account mirrors the credential record FMD2 exposes as MODULE.Account.
type Account struct {
	Enabled  bool
	Username string
	Password string
	Cookies  string
	Status   int
}

// selectSite picks the declaration a caller asked for.
//
// An empty name takes the first, which is what a file declaring one website
// means. Mirrors share a name and differ only by address, so rootURL breaks
// the tie; asking for an address that is no longer declared falls back to the
// site's first mirror rather than failing, because a mirror disappearing
// upstream should not take a followed series with it.
func selectSite(sites []*Module, name, rootURL string) *Module {
	if name == "" {
		return sites[0]
	}
	var matched []*Module
	for _, m := range sites {
		if strings.EqualFold(m.Name, name) {
			matched = append(matched, m)
		}
	}
	if len(matched) == 0 {
		return nil
	}
	for _, m := range matched {
		if rootURL != "" && strings.EqualFold(m.RootURL, rootURL) {
			return m
		}
	}
	return matched[0]
}

// Entry is one manga in a site's directory listing.
type Entry struct {
	Link string
	Name string
}

// Runner is one module bound to one Lua runtime.
//
// A runtime is not safe for concurrent use and the module API is built on
// globals (URL, LINKS, MANGAINFO), so each scrape takes its own Runner. The
// worker pool owns the concurrency; a Runner is single-threaded by contract.
type Runner struct {
	lua *rt.Runtime
	// ctx stops the runner: no Lua call starts once it is done, and bindings
	// end the running call through checkContext.
	ctx context.Context
	// cpuLimit caps the VM ticks of each call; see luaCPULimit. peakCPU is
	// the most any one call has used, which is how the limit is calibrated.
	cpuLimit uint64
	peakCPU  uint64
	// mod is the website this runner is scraping; sites is everything the
	// file declared, because one file is not one website.
	mod   *Module
	sites []*Module
	http  *HTTP

	mangaInfo *MangaInfo
	task      *Task
	links     *Strings
	names     *Strings
	update    *updateList
	options   map[string]rt.Value
	// storage backs MODULE.Storage, a per-module key/value scratchpad that some
	// templates consult to decide between parsing strategies.
	storage map[string]string
	// account backs MODULE.Account. Until SetAccount supplies credentials it
	// reports Enabled=false, so login handlers decline cleanly instead of
	// faulting on a nil field.
	account *Account
	// directoryIndex backs MODULE.CurrentDirectoryIndex, which multi-category
	// sites read to decide which listing to walk.
	directoryIndex int
	// imageFile is the page OnAfterImageSaved is editing, the one file io.open
	// may write; it is empty at any other time.
	imageFile string
}

// luaCPULimit caps the VM ticks one call into a module may use: loading its
// file, Init, or a handler. golua runs roughly 140 million ticks a second of
// pure Lua, so this is about seven seconds of it; time spent in Go (HTTP
// waits, XPath) costs nothing.
//
// Measured under golua: the heaviest call in the golden and recorded tests
// uses about 52 thousand ticks, and decoding 1.1 MB of JSON with upstream's
// pure-Lua utils/json about 45 million. The limit leaves room for a response
// twenty times that size, and still stops a runaway loop within seconds.
// TestCPULimitHeadroom keeps both margins.
const luaCPULimit = 1_000_000_000

// Open loads a module file and prepares a runtime for one of the websites it
// declares.
//
// A file is not a website. Twenty-eight of them declare several, either
// genuinely different sites — E-Hentai and ExHentai share a file and only one
// of them takes a login — or mirrors of one site under a dozen domains. Site
// selects by declared name; an empty name takes the first declaration, and
// rootURL picks between mirrors that share a name. Everything the file
// declares is kept, so a caller can ask what else is in there.
func (h *Host) Open(ctx context.Context, moduleFile, site, rootURL string) (*Runner, error) {
	var r *Runner
	vm := newLuaRuntime(os.Stdout, h.LuaDir, func() string { return r.imageFile })
	r = &Runner{
		lua:       vm,
		ctx:       ctx,
		cpuLimit:  luaCPULimit,
		http:      NewHTTP(ctx, h.Limiter, h.Transport, h.Solver),
		mangaInfo: NewMangaInfo(),
		task:      NewTask(),
		links:     NewStrings(),
		names:     NewStrings(),
		update:    &updateList{},
		options:   map[string]rt.Value{},
		storage:   map[string]string{},
		account:   &Account{Status: asUnknown},
	}
	env := vm.GlobalEnv()
	preloadLibs(vm, h.LuaDir, func() string { return r.imageFile })
	registerBuiltins(vm, ctx)
	for name, v := range map[string]rt.Value{
		"HTTP":       r.http.bind(vm, r.checkContext),
		"MANGAINFO":  r.mangaInfo.bind(vm),
		"TASK":       r.task.bind(vm),
		"LINKS":      pushStrings(vm, r.links),
		"NAMES":      pushStrings(vm, r.names),
		"UPDATELIST": r.update.bind(vm),
		"PAGENUMBER": rt.IntValue(1),
	} {
		env.Set(rt.StringValue(name), v)
	}

	// Real modules populate the table NewWebsiteModule returns and fall off the
	// end of Init() without returning it, despite what LUA-REFERENCE.md shows.
	// Capture the tables here so either shape works.
	//
	// Each declaration collects its own options. They used to share one slice,
	// so a file declaring two websites showed both sites' settings on each of
	// them — the same dropdown, twice.
	type declaration struct {
		tbl  *rt.Table
		opts []Option
	}
	var decls []*declaration
	setGoFunc(vm, env, "NewWebsiteModule", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		d := &declaration{}
		d.tbl = newWebsiteModule(t.Runtime, &d.opts, r.storage)
		decls = append(decls, d)
		return c.PushingNext1(t.Runtime, rt.TableValue(d.tbl)), nil
	}, 0, true)

	if err := r.protect(func(t *rt.Thread) error { return doModuleFile(t, moduleFile) }); err != nil {
		return nil, fmt.Errorf("load %s: %w", moduleFile, err)
	}
	var ret rt.Value
	if err := r.protect(func(t *rt.Thread) (err error) {
		ret, err = rt.Call1(t, env.Get(rt.StringValue("Init")))
		return err
	}); err != nil {
		return nil, fmt.Errorf("Init %s: %w", moduleFile, err)
	}
	// A module that returns its table from Init() is the documented shape, and
	// a handful do; it is the same single declaration either way.
	if tbl, ok := ret.TryTable(); ok && len(decls) == 0 {
		decls = append(decls, &declaration{tbl: tbl})
	}
	if len(decls) == 0 {
		return nil, fmt.Errorf("%s: Init never called NewWebsiteModule", moduleFile)
	}

	for _, d := range decls {
		r.sites = append(r.sites, readModule(d.tbl, d.opts, moduleFile))
	}
	r.mod = selectSite(r.sites, site, rootURL)
	if r.mod == nil {
		return nil, fmt.Errorf("%s declares no website named %q", moduleFile, site)
	}
	for _, o := range r.mod.Options {
		r.options[o.Name] = luaValueOf(o.Default)
	}
	env.Set(rt.StringValue("MODULE"), r.bindModule())
	return r, nil
}

// doModuleFile loads and runs a module file.
//
// LoadFromSourceOrCode with stripComment skips a UTF-8 byte-order mark and a
// leading "#" line, as Lua's own luaL_loadfile does. Seventeen upstream
// modules start with a byte-order mark. Only text is accepted: a module file
// is never a precompiled chunk.
func doModuleFile(t *rt.Thread, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	chunk, err := t.LoadFromSourceOrCode(path, src, "t", rt.TableValue(t.GlobalEnv()), true)
	if err != nil {
		return err
	}
	_, err = rt.Call1(t, rt.FunctionValue(chunk))
	return err
}

// protect runs one call into the module under the CPU limit, and not at all
// once ctx is done.
//
// golua cannot be interrupted from another goroutine, so cancellation takes
// effect at the next binding that calls checkContext, or at the CPU limit for
// code that calls none. When ctx is done by the time the call returns, the
// error wraps ctx.Err() even if the module caught the termination and
// returned normally, so callers can tell a shutdown from a module fault and
// never take a cancelled call's result as real.
func (r *Runner) protect(f func(t *rt.Thread) error) error {
	if err := r.ctx.Err(); err != nil {
		return err
	}
	t := r.lua.MainThread()
	used, err := t.CallContext(rt.RuntimeContextDef{
		HardLimits: rt.RuntimeResources{Cpu: r.cpuLimit},
	}, func() error { return f(t) })
	if used != nil {
		r.peakCPU = max(r.peakCPU, used.UsedResources().Cpu)
	}
	if r.ctx.Err() != nil {
		return fmt.Errorf("stopped: %w", r.ctx.Err())
	}
	var term rt.ContextTerminationError
	if errors.As(err, &term) {
		return fmt.Errorf("%w; the module ran too long without returning", err)
	}
	return err
}

// checkContext ends the running call when ctx is done. Bindings that can be
// reached in a loop, above all HTTP, call it on entry.
//
// golua runs pcall in a nested context, so a termination raised inside a
// pcall ends only that pcall. Upstream modules use pcall solely around JSON
// decoding, never around a binding in a loop, so in practice the call ends at
// the next binding; a loop that did catch it still stops at the CPU limit.
func (r *Runner) checkContext(t *rt.Thread) {
	if err := r.ctx.Err(); err != nil {
		t.TerminateContext("%v", err)
	}
}

// call invokes the Lua function bound to an event and returns its result.
func (r *Runner) call(event string) (rt.Value, error) {
	name, ok := r.mod.Handler(event)
	if !ok {
		return rt.NilValue, fmt.Errorf("module %s has no %s handler", r.mod.Name, event)
	}
	fn := r.lua.GlobalEnv().Get(rt.StringValue(name))
	if fn.IsNil() {
		return rt.NilValue, fmt.Errorf("module %s declares %s=%q but defines no such function",
			r.mod.Name, event, name)
	}
	var v rt.Value
	if err := r.protect(func(t *rt.Thread) (err error) {
		v, err = rt.Call1(t, fn)
		return err
	}); err != nil {
		return rt.NilValue, fmt.Errorf("%s/%s: %w", r.mod.Name, name, err)
	}
	return v, nil
}

// Close releases the runner. A golua runtime holds nothing that needs an
// explicit release, but callers close every runner they open, so a resource
// added later has somewhere to be freed.
func (r *Runner) Close() {}

// Module returns the loaded module's metadata.
func (r *Runner) Module() *Module { return r.mod }

// Sites returns every website the module file declared.
func (r *Runner) Sites() []*Module { return r.sites }

// SetAccount supplies credentials for a module that implements OnLogin.
func (r *Runner) SetAccount(username, password string) {
	r.account.Enabled = username != ""
	r.account.Username = username
	r.account.Password = password
}

// SetDirectoryIndex selects which listing a multi-section module walks.
func (r *Runner) SetDirectoryIndex(i int) { r.directoryIndex = i }

// TotalDirectories is how many separate listings the site is split into.
//
// A module declares this when its directory is not one list: an alphabet,
// where each letter is its own index, or a handful of sections such as
// ongoing, finished and one-shots. Most declare nothing, which means one.
func (r *Runner) TotalDirectories() int {
	if r.mod == nil || r.mod.TotalDirectory < 1 {
		return 1
	}
	return r.mod.TotalDirectory
}

// SetOptionString stores an override from its text form, converting it to the
// type the module declared. A checkbox read back as the string "true" would be
// truthy either way, but a spin edit compared with a number would not.
func (r *Runner) SetOptionString(name, value string) {
	kind := OptionEditBox
	for _, o := range r.mod.Options {
		if o.Name == name {
			kind = o.Kind
			break
		}
	}
	switch kind {
	case OptionCheckBox:
		r.options[name] = rt.BoolValue(value == "1" || strings.EqualFold(value, "true"))
	case OptionSpinEdit, OptionComboBox:
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			r.options[name] = rt.IntValue(int64(n))
			return
		}
		r.options[name] = rt.StringValue(value)
	default:
		r.options[name] = rt.StringValue(value)
	}
}

// Options returns the settings the module declared.
func (r *Runner) Options() []Option { return r.mod.Options }

// OnStatus registers a callback for UPDATELIST.UpdateStatusText, which modules
// use to report progress while walking a long directory.
func (r *Runner) OnStatus(fn func(string)) { r.update.onStatus = fn }

// setGlobal sets a global the next handler reads, such as URL.
func (r *Runner) setGlobal(name string, v rt.Value) {
	r.lua.GlobalEnv().Set(rt.StringValue(name), v)
}

// GetNameAndLink runs OnGetNameAndLink for one directory page. page is 0-based,
// matching the URL global the modules read.
func (r *Runner) GetNameAndLink(page int) ([]Entry, error) {
	r.links.Clear()
	r.names.Clear()
	r.setGlobal("URL", rt.IntValue(int64(page)))

	v, err := r.call("OnGetNameAndLink")
	if err != nil {
		return nil, err
	}
	if truncInt(v) == netProblem {
		return nil, fmt.Errorf("%s: network problem listing page %d from %s (last status %d)",
			r.mod.Name, page+1, r.mod.RootURL, r.http.ResultCode)
	}

	links, names := r.links.All(), r.names.All()
	out := make([]Entry, 0, len(links))
	for i, l := range links {
		e := Entry{Link: NormaliseLink(l)}
		if i < len(names) {
			e.Name = names[i]
		}
		out = append(out, e)
	}
	return out, nil
}

// GetDirectoryPageNumber runs OnGetDirectoryPageNumber, returning 1 when the
// module does not implement it.
func (r *Runner) GetDirectoryPageNumber() (int, error) {
	if _, ok := r.mod.Handler("OnGetDirectoryPageNumber"); !ok {
		return 1, nil
	}
	r.setGlobal("PAGENUMBER", rt.IntValue(1))
	if _, err := r.call("OnGetDirectoryPageNumber"); err != nil {
		return 0, err
	}
	return max(truncInt(r.lua.GlobalEnv().Get(rt.StringValue("PAGENUMBER"))), 1), nil
}

// GetInfo runs OnGetInfo for one manga URL.
func (r *Runner) GetInfo(mangaURL string) (*MangaInfo, error) {
	r.mangaInfo.ChapterLinks.Clear()
	r.mangaInfo.ChapterNames.Clear()
	r.mangaInfo.URL = mangaURL
	r.setGlobal("URL", rt.StringValue(mangaURL))

	v, err := r.call("OnGetInfo")
	if err != nil {
		return nil, err
	}
	switch truncInt(v) {
	case netProblem:
		return nil, fmt.Errorf("%s: network problem fetching %s (last status %d)",
			r.mod.Name, mangaURL, r.http.ResultCode)
	case informationNotFound:
		return nil, fmt.Errorf("%s: no series information at %s; the page may have moved",
			r.mod.Name, mangaURL)
	}
	// A chapter link is handed straight back to the module later, so it has
	// to be in the shape the module expects to receive.
	chapters := r.mangaInfo.ChapterLinks.All()
	for i, l := range chapters {
		chapters[i] = NormaliseLink(l)
	}
	r.mangaInfo.ChapterLinks.Set(chapters)
	return r.mangaInfo, nil
}

// GetPageNumber runs OnGetPageNumber, returning the chapter's image URLs.
func (r *Runner) GetPageNumber(chapterURL string) ([]string, error) {
	r.task.PageLinks.Clear()
	r.task.PageContainerLinks.Clear()
	r.task.Link = chapterURL
	r.setGlobal("URL", rt.StringValue(chapterURL))

	v, err := r.call("OnGetPageNumber")
	if err != nil {
		return nil, err
	}
	if !rt.Truth(v) {
		return nil, fmt.Errorf("%s: could not read pages for %s (last status %d)",
			r.mod.Name, chapterURL, r.http.ResultCode)
	}
	return r.task.PageLinks.All(), nil
}

// HasHandler reports whether the module implements an event.
func (r *Runner) HasHandler(event string) bool {
	_, ok := r.mod.Handler(event)
	return ok
}

// BeforeDownloadImage runs OnBeforeDownloadImage and returns the request
// headers the module set.
//
// 113 modules implement this, almost always to set a Referer that the image
// host requires. Skipping it yields a 403 or a placeholder image rather than an
// obvious failure.
func (r *Runner) BeforeDownloadImage(imageURL string) (map[string]string, error) {
	if !r.HasHandler("OnBeforeDownloadImage") {
		return nil, nil
	}
	r.http.Headers.Clear()
	r.setGlobal("URL", rt.StringValue(imageURL))
	if _, err := r.call("OnBeforeDownloadImage"); err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for _, raw := range r.http.Headers.All() {
		if k, v, ok := strings.Cut(raw, "="); ok {
			headers[strings.TrimSpace(k)] = v
		}
	}
	return headers, nil
}

// DownloadImage runs OnDownloadImage, returning the bytes the module produced.
//
// Modules implementing this fetch the image themselves and may transform it
// before handing it back — descrambling a tiled image, for instance — so the
// result is taken from HTTP.Document rather than fetched again by the host.
func (r *Runner) DownloadImage(imageURL string) ([]byte, error) {
	r.http.Document.Set(nil)
	r.setGlobal("URL", rt.StringValue(imageURL))
	v, err := r.call("OnDownloadImage")
	if err != nil {
		return nil, err
	}
	if !rt.Truth(v) {
		return nil, fmt.Errorf("%s: module declined to download %s", r.mod.Name, imageURL)
	}
	data := r.http.Document.Bytes()
	if len(data) == 0 {
		return nil, fmt.Errorf("%s: module returned no image data for %s", r.mod.Name, imageURL)
	}
	return data, nil
}

// Login runs the module's OnLogin handler.
//
// It reports whether the module considered the login successful. The account
// state the handler sets is also checked, because several modules return true
// while recording asInvalid.
func (r *Runner) Login() (bool, error) {
	if !r.HasHandler("OnLogin") {
		return false, fmt.Errorf("module %s has no login handler", r.mod.Name)
	}
	if !r.account.Enabled {
		return false, fmt.Errorf("module %s has no credentials configured", r.mod.Name)
	}
	v, err := r.call("OnLogin")
	if err != nil {
		return false, err
	}
	if r.account.Status == asInvalid {
		return false, fmt.Errorf("module %s rejected the credentials", r.mod.Name)
	}
	return rt.Truth(v), nil
}

// AccountStatus reports the state the module recorded during login.
func (r *Runner) AccountStatus() int { return r.account.Status }

// AfterImageSaved runs the module's OnAfterImageSaved handler against a file
// on disk.
//
// FMD2 writes each page out before packing, so the handler is given a path and
// edits the file in place — removing a watermark, for instance. atsume keeps
// pages in memory, so a file is materialised only for the modules that declare
// this, and the result is read back.
func (r *Runner) AfterImageSaved(path string) error {
	if !r.HasHandler("OnAfterImageSaved") {
		return nil
	}
	r.setGlobal("FILENAME", rt.StringValue(path))
	r.imageFile = path
	defer func() { r.imageFile = "" }()
	_, err := r.call("OnAfterImageSaved")
	return err
}

// bind exposes the account as MODULE.Account.
func (a *Account) bind(r *rt.Runtime) rt.Value {
	f := newFields("atsume.Account")
	f.boolean["Enabled"] = &a.Enabled
	f.str["Username"] = &a.Username
	f.str["Password"] = &a.Password
	f.str["Cookies"] = &a.Cookies
	f.num["Status"] = &a.Status
	return f.push(r)
}

// bindModule exposes the selected site as MODULE.
func (r *Runner) bindModule() rt.Value {
	f := newFields("atsume.Module")
	f.str["ID"] = &r.mod.ID
	f.str["Name"] = &r.mod.Name
	f.str["RootURL"] = &r.mod.RootURL
	f.str["Category"] = &r.mod.Category
	f.num["TotalDirectory"] = &r.mod.TotalDirectory
	f.boolean["AccountSupport"] = &r.mod.AccountSupport
	f.num["CurrentDirectoryIndex"] = &r.directoryIndex
	f.methods["GetOption"] = goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		name, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, r.options[name]), nil
	}}
	f.getter["Storage"] = func(t *rt.Thread) rt.Value { return pushStorage(r.storage) }
	f.getter["Account"] = func(t *rt.Thread) rt.Value { return r.account.bind(t.Runtime) }
	f.getter["ActiveConnectionCount"] = func(t *rt.Thread) rt.Value { return rt.IntValue(1) }

	// Cookie management is delegated to the HTTP object's jar; these exist
	// because a handful of modules reset cookies through MODULE rather than HTTP.
	f.methods["ClearCookies"] = noResult(0, func(*rt.GoCont) error { r.http.Cookies.Clear(); return nil })
	f.methods["RemoveCookies"] = noResult(0, func(*rt.GoCont) error { r.http.Cookies.Clear(); return nil })
	f.methods["AddServerCookies"] = noResult(2, func(c *rt.GoCont) error {
		s, err := checkString(c, 1)
		if err == nil {
			r.http.Cookies.Add(s)
		}
		return err
	})
	return f.push(r.lua)
}
