package scraper

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	lua "github.com/yuin/gopher-lua"
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

// Runner is one module bound to one Lua state.
//
// A state is not safe for concurrent use and the module API is built on
// globals (URL, LINKS, MANGAINFO), so each scrape takes its own Runner. The
// worker pool owns the concurrency; a Runner is single-threaded by contract.
type Runner struct {
	L *lua.LState
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
	options   map[string]lua.LValue
	// storage backs MODULE.Storage, a per-module key/value scratchpad that some
	// templates consult to decide between parsing strategies.
	storage map[string]string
	// account backs MODULE.Account. Credentials are not implemented yet, so it
	// reports Enabled=false and login handlers decline cleanly instead of
	// faulting on a nil field.
	account *Account
	// directoryIndex backs MODULE.CurrentDirectoryIndex, which multi-category
	// sites read to decide which listing to walk.
	directoryIndex int
}

// Account mirrors the credential record FMD2 exposes as MODULE.Account.
type Account struct {
	Enabled  bool
	Username string
	Password string
	Cookies  string
	Status   int
}

func (a *Account) bind(L *lua.LState) lua.LValue {
	f := newFields("atsume.Account")
	f.boolean["Enabled"] = &a.Enabled
	f.str["Username"] = &a.Username
	f.str["Password"] = &a.Password
	f.str["Cookies"] = &a.Cookies
	f.num["Status"] = &a.Status
	return f.push(L)
}

// Open loads a module file and prepares a state for one of the websites it
// declares.
//
// A file is not a website. Twenty-eight of them declare several, either
// genuinely different sites — E-Hentai and ExHentai share a file and only one
// of them takes a login — or mirrors of one site under a dozen domains. Site
// selects by declared name; an empty name takes the first declaration, and
// rootURL picks between mirrors that share a name. Everything the file
// declares is kept, so a caller can ask what else is in there.
func (h *Host) Open(ctx context.Context, moduleFile, site, rootURL string) (*Runner, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: false})
	L.SetContext(ctx)

	r := &Runner{
		L:         L,
		http:      NewHTTP(ctx, h.Limiter, h.Transport, h.Solver),
		mangaInfo: NewMangaInfo(),
		task:      NewTask(),
		links:     NewStrings(),
		names:     NewStrings(),
		update:    &updateList{},
		options:   map[string]lua.LValue{},
		storage:   map[string]string{},
		account:   &Account{Status: asUnknown},
	}

	// `require 'templates.Madara'` resolves against the checkout; the ?/init.lua
	// entry is unused by FMD2 but costs nothing and matches Lua convention.
	pkg := L.GetGlobal("package").(*lua.LTable)
	L.SetField(pkg, "path", lua.LString(
		filepath.Join(h.LuaDir, "?.lua")+";"+filepath.Join(h.LuaDir, "?", "init.lua")))

	registerDocument(L)
	registerImagePuzzle(L)
	registerStrings(L)
	registerValues(L)
	registerQuery(L)
	registerNode(L)
	registerNodeList(L)
	registerBuiltins(L)
	registerLibs(L, h.LuaDir)

	L.SetGlobal("HTTP", r.http.bind(L))
	L.SetGlobal("MANGAINFO", r.mangaInfo.bind(L))
	L.SetGlobal("TASK", r.task.bind(L))
	L.SetGlobal("LINKS", pushStrings(L, r.links))
	L.SetGlobal("NAMES", pushStrings(L, r.names))
	L.SetGlobal("UPDATELIST", r.update.bind(L))
	L.SetGlobal("PAGENUMBER", lua.LNumber(1))

	// Real modules populate the table NewWebsiteModule returns and fall off the
	// end of Init() without returning it, despite what LUA-REFERENCE.md shows.
	// Capture the tables here so either shape works.
	//
	// Each declaration collects its own options. They used to share one slice,
	// so a file declaring two websites showed both sites' settings on each of
	// them — the same dropdown, twice.
	type declaration struct {
		tbl  *lua.LTable
		opts []Option
	}
	var decls []*declaration
	L.SetGlobal("NewWebsiteModule", L.NewFunction(func(L *lua.LState) int {
		d := &declaration{}
		d.tbl = newWebsiteModule(L, &d.opts, r.storage)
		decls = append(decls, d)
		L.Push(d.tbl)
		return 1
	}))

	if err := doModuleFile(L, moduleFile); err != nil {
		L.Close()
		return nil, fmt.Errorf("load %s: %w", moduleFile, err)
	}
	if err := L.CallByParam(lua.P{Fn: L.GetGlobal("Init"), NRet: 1, Protect: true}); err != nil {
		L.Close()
		return nil, fmt.Errorf("Init %s: %w", moduleFile, err)
	}
	// A module that returns its table from Init() is the documented shape, and
	// a handful do; it is the same single declaration either way.
	if tbl, _ := L.Get(-1).(*lua.LTable); tbl != nil && len(decls) == 0 {
		decls = append(decls, &declaration{tbl: tbl})
	}
	L.Pop(1)
	if len(decls) == 0 {
		L.Close()
		return nil, fmt.Errorf("%s: Init never called NewWebsiteModule", moduleFile)
	}

	for _, d := range decls {
		r.sites = append(r.sites, readModule(d.tbl, d.opts, moduleFile))
	}
	r.mod = selectSite(r.sites, site, rootURL)
	if r.mod == nil {
		L.Close()
		return nil, fmt.Errorf("%s declares no website named %q", moduleFile, site)
	}
	for _, o := range r.mod.Options {
		r.options[o.Name] = o.Default
	}
	L.SetGlobal("MODULE", r.bindModule(L))
	return r, nil
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

// doModuleFile loads and runs a module file.
//
// It does not use L.DoFile because a number of upstream modules are saved with
// a UTF-8 BOM, which gopher-lua's lexer rejects as an invalid token on line 1.
// Stripping it costs nothing and recovers those modules.
func doModuleFile(L *lua.LState, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	src = bytes.TrimPrefix(src, []byte{0xEF, 0xBB, 0xBF})

	fn, err := L.Load(bytes.NewReader(src), path)
	if err != nil {
		return err
	}
	L.Push(fn)
	return L.PCall(0, lua.MultRet, nil)
}

// Close releases the Lua state.
func (r *Runner) Close() { r.L.Close() }

// Module returns the loaded module's metadata.
func (r *Runner) Module() *Module { return r.mod }

// Sites returns every website the module file declares, in the order it
// declared them.
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

// SetOption overrides a module-declared setting before a handler runs.
func (r *Runner) SetOption(name string, v lua.LValue) { r.options[name] = v }

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
		r.options[name] = lua.LBool(value == "1" || strings.EqualFold(value, "true"))
	case OptionSpinEdit, OptionComboBox:
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			r.options[name] = lua.LNumber(n)
			return
		}
		r.options[name] = lua.LString(value)
	default:
		r.options[name] = lua.LString(value)
	}
}

// Options returns the settings the module declared.
func (r *Runner) Options() []Option { return r.mod.Options }

// OnStatus registers a callback for UPDATELIST.UpdateStatusText, which modules
// use to report progress while walking a long directory.
func (r *Runner) OnStatus(fn func(string)) { r.update.onStatus = fn }

func (r *Runner) bindModule(L *lua.LState) lua.LValue {
	f := newFields("atsume.Module")
	f.str["ID"] = &r.mod.ID
	f.str["Name"] = &r.mod.Name
	f.str["RootURL"] = &r.mod.RootURL
	f.str["Category"] = &r.mod.Category
	f.num["TotalDirectory"] = &r.mod.TotalDirectory
	f.boolean["AccountSupport"] = &r.mod.AccountSupport
	f.methods["GetOption"] = func(L *lua.LState) int {
		v, ok := r.options[L.CheckString(1)]
		if !ok || v == nil {
			L.Push(lua.LNil)
		} else {
			L.Push(v)
		}
		return 1
	}
	f.num["CurrentDirectoryIndex"] = &r.directoryIndex
	f.getter["Storage"] = func(L *lua.LState) lua.LValue { return pushStorage(L, r.storage) }
	f.getter["Account"] = func(L *lua.LState) lua.LValue { return r.account.bind(L) }
	f.getter["ActiveConnectionCount"] = func(L *lua.LState) lua.LValue { return lua.LNumber(1) }

	// Cookie management is delegated to the HTTP object's jar; these exist
	// because a handful of modules reset cookies through MODULE rather than HTTP.
	f.methods["ClearCookies"] = func(L *lua.LState) int {
		r.http.Cookies.Clear()
		return 0
	}
	f.methods["RemoveCookies"] = func(L *lua.LState) int {
		r.http.Cookies.Clear()
		return 0
	}
	f.methods["AddServerCookies"] = func(L *lua.LState) int {
		r.http.Cookies.Add(L.CheckString(2))
		return 0
	}
	return f.push(L)
}

// call invokes the Lua function bound to an event and returns its result.
func (r *Runner) call(event string) (lua.LValue, error) {
	name, ok := r.mod.Handler(event)
	if !ok {
		return nil, fmt.Errorf("module %s has no %s handler", r.mod.Name, event)
	}
	fn := r.L.GetGlobal(name)
	if fn == lua.LNil {
		return nil, fmt.Errorf("module %s declares %s=%q but defines no such function",
			r.mod.Name, event, name)
	}
	if err := r.L.CallByParam(lua.P{Fn: fn, NRet: 1, Protect: true}); err != nil {
		return nil, fmt.Errorf("%s/%s: %w", r.mod.Name, name, err)
	}
	v := r.L.Get(-1)
	r.L.Pop(1)
	return v, nil
}

// Entry is one manga in a site's directory listing.
type Entry struct {
	Link string
	Name string
}

// GetNameAndLink runs OnGetNameAndLink for one directory page. page is 0-based,
// matching the URL global the modules read.
func (r *Runner) GetNameAndLink(page int) ([]Entry, error) {
	r.links.Clear()
	r.names.Clear()
	r.L.SetGlobal("URL", lua.LNumber(page))

	v, err := r.call("OnGetNameAndLink")
	if err != nil {
		return nil, err
	}
	if code := int(lua.LVAsNumber(v)); code == netProblem {
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
	r.L.SetGlobal("PAGENUMBER", lua.LNumber(1))
	if _, err := r.call("OnGetDirectoryPageNumber"); err != nil {
		return 0, err
	}
	n := int(lua.LVAsNumber(r.L.GetGlobal("PAGENUMBER")))
	if n < 1 {
		n = 1
	}
	return n, nil
}

// GetInfo runs OnGetInfo for one manga URL.
func (r *Runner) GetInfo(mangaURL string) (*MangaInfo, error) {
	r.mangaInfo.ChapterLinks.Clear()
	r.mangaInfo.ChapterNames.Clear()
	r.mangaInfo.URL = mangaURL
	r.L.SetGlobal("URL", lua.LString(mangaURL))

	v, err := r.call("OnGetInfo")
	if err != nil {
		return nil, err
	}
	switch int(lua.LVAsNumber(v)) {
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
	r.L.SetGlobal("URL", lua.LString(chapterURL))

	v, err := r.call("OnGetPageNumber")
	if err != nil {
		return nil, err
	}
	if !lua.LVAsBool(v) {
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
	r.L.SetGlobal("URL", lua.LString(imageURL))

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
	r.L.SetGlobal("URL", lua.LString(imageURL))

	v, err := r.call("OnDownloadImage")
	if err != nil {
		return nil, err
	}
	if !lua.LVAsBool(v) {
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
	ok := lua.LVAsBool(v)
	if r.account.Status == asInvalid {
		return false, fmt.Errorf("module %s rejected the credentials", r.mod.Name)
	}
	return ok, nil
}

// AccountStatus reports the state the module recorded during login.
func (r *Runner) AccountStatus() int { return r.account.Status }

// AfterImageSaved runs the module's OnAfterImageSaved handler against a file on
// disk.
//
// FMD2 writes each page out before packing, so the handler is given a path and
// edits the file in place — removing a watermark, for instance. atsume keeps
// pages in memory, so a file is materialised only for the modules that declare
// this, and the result is read back.
func (r *Runner) AfterImageSaved(path string) error {
	if !r.HasHandler("OnAfterImageSaved") {
		return nil
	}
	r.L.SetGlobal("FILENAME", lua.LString(path))
	_, err := r.call("OnAfterImageSaved")
	return err
}
