package scraper

import (
	"fmt"
	"strconv"
	"strings"

	rt "github.com/arnodel/golua/runtime"
)

// The public methods of goluaRunner mirror Runner's one for one, so the swap
// is a rename. See Runner for why each behaves as it does.

// Close releases the runner. A golua runtime holds nothing that needs freeing;
// the method exists so the two runners are interchangeable.
func (gr *goluaRunner) Close() {}

// Module returns the loaded module's metadata.
func (gr *goluaRunner) Module() *Module { return gr.mod }

// Sites returns every website the module file declared.
func (gr *goluaRunner) Sites() []*Module { return gr.sites }

// SetAccount supplies the credentials exposed as MODULE.Account.
func (gr *goluaRunner) SetAccount(username, password string) {
	gr.account.Enabled = username != ""
	gr.account.Username = username
	gr.account.Password = password
}

// SetDirectoryIndex selects which listing a multi-directory site walks.
func (gr *goluaRunner) SetDirectoryIndex(i int) { gr.directoryIndex = i }

// TotalDirectories is how many separate listings the site is split into.
func (gr *goluaRunner) TotalDirectories() int {
	if gr.mod == nil || gr.mod.TotalDirectory < 1 {
		return 1
	}
	return gr.mod.TotalDirectory
}

// SetOptionString stores an override from its text form, converting it to the
// type the module declared.
func (gr *goluaRunner) SetOptionString(name, value string) {
	kind := OptionEditBox
	for _, o := range gr.mod.Options {
		if o.Name == name {
			kind = o.Kind
			break
		}
	}
	switch kind {
	case OptionCheckBox:
		gr.options[name] = rt.BoolValue(value == "1" || strings.EqualFold(value, "true"))
	case OptionSpinEdit, OptionComboBox:
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
			gr.options[name] = rt.IntValue(int64(n))
			return
		}
		gr.options[name] = rt.StringValue(value)
	default:
		gr.options[name] = rt.StringValue(value)
	}
}

// Options returns the settings the module declared.
func (gr *goluaRunner) Options() []Option { return gr.mod.Options }

// OnStatus registers a callback for UPDATELIST.UpdateStatusText.
func (gr *goluaRunner) OnStatus(fn func(string)) { gr.update.onStatus = fn }

// setGlobal sets a global the next handler reads, such as URL.
func (gr *goluaRunner) setGlobal(name string, v rt.Value) {
	gr.r.GlobalEnv().Set(rt.StringValue(name), v)
}

// GetNameAndLink runs OnGetNameAndLink for one 0-based directory page.
func (gr *goluaRunner) GetNameAndLink(page int) ([]Entry, error) {
	gr.links.Clear()
	gr.names.Clear()
	gr.setGlobal("URL", rt.IntValue(int64(page)))

	v, err := gr.call("OnGetNameAndLink")
	if err != nil {
		return nil, err
	}
	if truncInt(v) == netProblem {
		return nil, fmt.Errorf("%s: network problem listing page %d from %s (last status %d)",
			gr.mod.Name, page+1, gr.mod.RootURL, gr.http.ResultCode)
	}

	links, names := gr.links.All(), gr.names.All()
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
func (gr *goluaRunner) GetDirectoryPageNumber() (int, error) {
	if _, ok := gr.mod.Handler("OnGetDirectoryPageNumber"); !ok {
		return 1, nil
	}
	gr.setGlobal("PAGENUMBER", rt.IntValue(1))
	if _, err := gr.call("OnGetDirectoryPageNumber"); err != nil {
		return 0, err
	}
	return max(truncInt(gr.r.GlobalEnv().Get(rt.StringValue("PAGENUMBER"))), 1), nil
}

// GetInfo runs OnGetInfo for one manga URL.
func (gr *goluaRunner) GetInfo(mangaURL string) (*MangaInfo, error) {
	gr.mangaInfo.ChapterLinks.Clear()
	gr.mangaInfo.ChapterNames.Clear()
	gr.mangaInfo.URL = mangaURL
	gr.setGlobal("URL", rt.StringValue(mangaURL))

	v, err := gr.call("OnGetInfo")
	if err != nil {
		return nil, err
	}
	switch truncInt(v) {
	case netProblem:
		return nil, fmt.Errorf("%s: network problem fetching %s (last status %d)",
			gr.mod.Name, mangaURL, gr.http.ResultCode)
	case informationNotFound:
		return nil, fmt.Errorf("%s: no series information at %s; the page may have moved",
			gr.mod.Name, mangaURL)
	}
	chapters := gr.mangaInfo.ChapterLinks.All()
	for i, l := range chapters {
		chapters[i] = NormaliseLink(l)
	}
	gr.mangaInfo.ChapterLinks.Set(chapters)
	return gr.mangaInfo, nil
}

// GetPageNumber runs OnGetPageNumber, returning the chapter's image URLs.
func (gr *goluaRunner) GetPageNumber(chapterURL string) ([]string, error) {
	gr.task.PageLinks.Clear()
	gr.task.PageContainerLinks.Clear()
	gr.task.Link = chapterURL
	gr.setGlobal("URL", rt.StringValue(chapterURL))

	v, err := gr.call("OnGetPageNumber")
	if err != nil {
		return nil, err
	}
	if !rt.Truth(v) {
		return nil, fmt.Errorf("%s: could not read pages for %s (last status %d)",
			gr.mod.Name, chapterURL, gr.http.ResultCode)
	}
	return gr.task.PageLinks.All(), nil
}

// HasHandler reports whether the module implements an event.
func (gr *goluaRunner) HasHandler(event string) bool {
	_, ok := gr.mod.Handler(event)
	return ok
}

// BeforeDownloadImage runs OnBeforeDownloadImage and returns the request
// headers the module set.
func (gr *goluaRunner) BeforeDownloadImage(imageURL string) (map[string]string, error) {
	if !gr.HasHandler("OnBeforeDownloadImage") {
		return nil, nil
	}
	gr.http.Headers.Clear()
	gr.setGlobal("URL", rt.StringValue(imageURL))
	if _, err := gr.call("OnBeforeDownloadImage"); err != nil {
		return nil, err
	}
	headers := map[string]string{}
	for _, raw := range gr.http.Headers.All() {
		if k, v, ok := strings.Cut(raw, "="); ok {
			headers[strings.TrimSpace(k)] = v
		}
	}
	return headers, nil
}

// DownloadImage runs OnDownloadImage, returning the bytes the module produced.
func (gr *goluaRunner) DownloadImage(imageURL string) ([]byte, error) {
	gr.http.Document.Set(nil)
	gr.setGlobal("URL", rt.StringValue(imageURL))
	v, err := gr.call("OnDownloadImage")
	if err != nil {
		return nil, err
	}
	if !rt.Truth(v) {
		return nil, fmt.Errorf("%s: module declined to download %s", gr.mod.Name, imageURL)
	}
	data := gr.http.Document.Bytes()
	if len(data) == 0 {
		return nil, fmt.Errorf("%s: module returned no image data for %s", gr.mod.Name, imageURL)
	}
	return data, nil
}

// Login runs the module's OnLogin handler.
func (gr *goluaRunner) Login() (bool, error) {
	if !gr.HasHandler("OnLogin") {
		return false, fmt.Errorf("module %s has no login handler", gr.mod.Name)
	}
	if !gr.account.Enabled {
		return false, fmt.Errorf("module %s has no credentials configured", gr.mod.Name)
	}
	v, err := gr.call("OnLogin")
	if err != nil {
		return false, err
	}
	if gr.account.Status == asInvalid {
		return false, fmt.Errorf("module %s rejected the credentials", gr.mod.Name)
	}
	return rt.Truth(v), nil
}

// AccountStatus reports the state the module recorded during login.
func (gr *goluaRunner) AccountStatus() int { return gr.account.Status }

// AfterImageSaved runs the module's OnAfterImageSaved handler against a file
// on disk.
func (gr *goluaRunner) AfterImageSaved(path string) error {
	if !gr.HasHandler("OnAfterImageSaved") {
		return nil
	}
	gr.setGlobal("FILENAME", rt.StringValue(path))
	_, err := gr.call("OnAfterImageSaved")
	return err
}
