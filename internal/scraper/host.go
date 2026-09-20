package scraper

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
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
}

// Runner is one module bound to one Lua state.
//
// A state is not safe for concurrent use and the module API is built on
// globals (URL, LINKS, MANGAINFO), so each scrape takes its own Runner. The
// worker pool owns the concurrency; a Runner is single-threaded by contract.
type Runner struct {
	L    *lua.LState
	mod  *Module
	http *HTTP

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

// Open loads one module file and prepares a state for it.
func (h *Host) Open(ctx context.Context, moduleFile string) (*Runner, error) {
	L := lua.NewState(lua.Options{SkipOpenLibs: false})
	L.SetContext(ctx)

	r := &Runner{
		L:         L,
		http:      NewHTTP(ctx, h.Limiter),
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
	// Capture the table here so either shape works.
	var opts []Option
	var declared *lua.LTable
	L.SetGlobal("NewWebsiteModule", L.NewFunction(func(L *lua.LState) int {
		declared = newWebsiteModule(L, &opts, r.storage)
		L.Push(declared)
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
	tbl, _ := L.Get(-1).(*lua.LTable)
	L.Pop(1)
	if tbl == nil {
		tbl = declared
	}
	if tbl == nil {
		L.Close()
		return nil, fmt.Errorf("%s: Init never called NewWebsiteModule", moduleFile)
	}

	r.mod = readModule(tbl, opts, moduleFile)
	for _, o := range r.mod.Options {
		r.options[o.Name] = o.Default
	}
	L.SetGlobal("MODULE", r.bindModule(L))
	return r, nil
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

// SetAccount supplies credentials for a module that implements OnLogin.
func (r *Runner) SetAccount(username, password string) {
	r.account.Enabled = username != ""
	r.account.Username = username
	r.account.Password = password
}

// SetDirectoryIndex selects which listing a multi-category module walks.
func (r *Runner) SetDirectoryIndex(i int) { r.directoryIndex = i }

// SetOption overrides a module-declared setting before a handler runs.
func (r *Runner) SetOption(name string, v lua.LValue) { r.options[name] = v }

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
		return nil, fmt.Errorf("%s: network problem listing page %d", r.mod.Name, page)
	}

	links, names := r.links.All(), r.names.All()
	out := make([]Entry, 0, len(links))
	for i, l := range links {
		e := Entry{Link: l}
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
		return nil, fmt.Errorf("%s: network problem fetching %s", r.mod.Name, mangaURL)
	case informationNotFound:
		return nil, fmt.Errorf("%s: no information at %s", r.mod.Name, mangaURL)
	}
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
		return nil, fmt.Errorf("%s: could not read pages for %s", r.mod.Name, chapterURL)
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
