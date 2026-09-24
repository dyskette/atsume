package scraper

import (
	"net/http"
	"strings"

	rt "github.com/arnodel/golua/runtime"
)

// This file binds the handler-time objects to golua: the counterparts of the
// bind methods in context.go, http.go and host.go. The Go types themselves
// are shared between the two runtimes.

func (m *MangaInfo) bindGolua(r *rt.Runtime) rt.Value {
	f := newGoluaFields("atsume.MangaInfo")
	f.str["URL"] = &m.URL
	f.str["Title"] = &m.Title
	f.str["AltTitles"] = &m.AltTitles
	f.str["CoverLink"] = &m.CoverLink
	f.str["Authors"] = &m.Authors
	f.str["Artists"] = &m.Artists
	f.str["Genres"] = &m.Genres
	f.str["Status"] = &m.Status
	f.str["Summary"] = &m.Summary
	f.list["ChapterLinks"] = m.ChapterLinks
	f.list["ChapterNames"] = m.ChapterNames
	return f.push(r)
}

func (t *Task) bindGolua(r *rt.Runtime) rt.Value {
	f := newGoluaFields("atsume.Task")
	f.str["Link"] = &t.Link
	f.num["PageNumber"] = &t.PageNumber
	f.num["CurrentDownloadChapterPtr"] = &t.CurrentDownloadChapterPtr
	f.list["PageLinks"] = t.PageLinks
	f.list["PageContainerLinks"] = t.PageContainerLinks
	f.list["FileNames"] = t.FileNames
	f.list["ChapterLinks"] = t.ChapterLinks
	f.list["ChapterNames"] = t.ChapterNames
	return f.push(r)
}

func (u *updateList) bindGolua(r *rt.Runtime) rt.Value {
	f := newGoluaFields("atsume.UpdateList")
	f.num["CurrentDirectoryPageNumber"] = &u.CurrentDirectoryPageNumber
	f.methods["UpdateStatusText"] = goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		s, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		if u.onStatus != nil {
			u.onStatus(s)
		}
		return c.Next(), nil
	}}
	return f.push(r)
}

func (a *Account) bindGolua(r *rt.Runtime) rt.Value {
	f := newGoluaFields("atsume.Account")
	f.boolean["Enabled"] = &a.Enabled
	f.str["Username"] = &a.Username
	f.str["Password"] = &a.Password
	f.str["Cookies"] = &a.Cookies
	f.num["Status"] = &a.Status
	return f.push(r)
}

// noResult adapts a Go function with no Lua result.
func noResult(nArgs int, fn func(c *rt.GoCont) error) goFn {
	return goFn{nArgs, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		if err := fn(c); err != nil {
			return nil, err
		}
		return c.Next(), nil
	}}
}

// bindGolua exposes the client as HTTP. Every request method checks the
// runner's context first, so a cancelled scrape ends at its next request
// instead of carrying on with failed ones.
func (h *HTTP) bindGolua(r *rt.Runtime, checkContext func(*rt.Thread)) rt.Value {
	f := newGoluaFields("atsume.HTTP")
	f.str["MimeType"] = &h.MimeType
	f.str["UserAgent"] = &h.UserAgent
	f.str["LastURL"] = &h.LastURL
	f.num["RetryCount"] = &h.RetryCount
	f.num["ResultCode"] = &h.ResultCode
	f.boolean["Terminated"] = &h.Terminated
	f.list["Headers"] = h.Headers
	f.list["Cookies"] = h.Cookies
	f.getter["Document"] = func(t *rt.Thread) rt.Value { return pushGoluaDocument(t.Runtime, h.Document) }

	// request adapts a request method: URL first, then an optional body.
	request := func(method string, before func()) goFn {
		return goFn{2, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			checkContext(t)
			u, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			body := ""
			if method == http.MethodPost {
				if body, err = optString(c, 1, ""); err != nil {
					return nil, err
				}
			}
			if before != nil {
				before()
			}
			return c.PushingNext1(t.Runtime, rt.BoolValue(h.do(method, u, body))), nil
		}}
	}
	f.methods["GET"] = request(http.MethodGet, nil)
	f.methods["POST"] = request(http.MethodPost, func() {
		if h.MimeType == "" {
			h.MimeType = "application/x-www-form-urlencoded"
		}
	})
	f.methods["HEAD"] = request(http.MethodHead, nil)
	// XHR is a GET that announces itself as an in-page request; several
	// modules rely on the server varying its response on this header.
	f.methods["XHR"] = request(http.MethodGet, func() {
		h.Headers.SetValue("X-Requested-With", "XMLHttpRequest")
	})
	f.methods["Reset"] = noResult(0, func(*rt.GoCont) error { h.Reset(); return nil })
	f.methods["ResetBasic"] = noResult(0, func(*rt.GoCont) error { h.Reset(); return nil })
	f.methods["ClearCookies"] = noResult(0, func(*rt.GoCont) error { h.Cookies.Clear(); return nil })
	f.methods["GetCookies"] = goFn{0, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		return c.PushingNext1(t.Runtime, rt.StringValue(strings.Join(h.Cookies.All(), "; "))), nil
	}}
	// The cookie is the second argument, as upstream's signature has it.
	f.methods["AddServerCookies"] = noResult(2, func(c *rt.GoCont) error {
		s, err := checkString(c, 1)
		if err == nil {
			h.Cookies.Add(s)
		}
		return err
	})
	f.methods["SetProxy"] = noResult(0, func(*rt.GoCont) error { return nil }) // proxying is host policy
	return f.push(r)
}

// bindModule exposes the selected site as MODULE.
func (gr *goluaRunner) bindModule() rt.Value {
	f := newGoluaFields("atsume.Module")
	f.str["ID"] = &gr.mod.ID
	f.str["Name"] = &gr.mod.Name
	f.str["RootURL"] = &gr.mod.RootURL
	f.str["Category"] = &gr.mod.Category
	f.num["TotalDirectory"] = &gr.mod.TotalDirectory
	f.boolean["AccountSupport"] = &gr.mod.AccountSupport
	f.num["CurrentDirectoryIndex"] = &gr.directoryIndex
	f.methods["GetOption"] = goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		name, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, gr.options[name]), nil
	}}
	f.getter["Storage"] = func(t *rt.Thread) rt.Value { return goluaStorage(gr.storage) }
	f.getter["Account"] = func(t *rt.Thread) rt.Value { return gr.account.bindGolua(t.Runtime) }
	f.getter["ActiveConnectionCount"] = func(t *rt.Thread) rt.Value { return rt.IntValue(1) }

	// Cookie management is delegated to the HTTP object's jar; these exist
	// because a handful of modules reset cookies through MODULE rather than HTTP.
	f.methods["ClearCookies"] = noResult(0, func(*rt.GoCont) error { gr.http.Cookies.Clear(); return nil })
	f.methods["RemoveCookies"] = noResult(0, func(*rt.GoCont) error { gr.http.Cookies.Clear(); return nil })
	f.methods["AddServerCookies"] = noResult(2, func(c *rt.GoCont) error {
		s, err := checkString(c, 1)
		if err == nil {
			gr.http.Cookies.Add(s)
		}
		return err
	})
	return f.push(gr.r)
}

// goluaOptionValue's inverse, for handing a default back through
// MODULE.GetOption.
func luaValueOf(v any) rt.Value {
	switch v := v.(type) {
	case bool:
		return rt.BoolValue(v)
	case int64:
		return rt.IntValue(v)
	case float64:
		return rt.FloatValue(v)
	case string:
		return rt.StringValue(v)
	}
	return rt.NilValue
}
