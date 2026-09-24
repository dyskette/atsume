package scraper

import (
	"io"
	"path/filepath"
	"reflect"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

// record is what the fields helper binds in these tests.
type record struct {
	Title  string
	Page   int
	Done   bool
	Links  *Strings
	Secret string // reached through a getter and setter
}

func newRecord() *record { return &record{Title: "t", Page: 1, Links: NewStrings()} }

// runGoluaFields runs src against a record bound with the golua helper.
func runGoluaFields(t *testing.T, rec *record, src string) (string, bool) {
	t.Helper()
	r := newLuaRuntime(io.Discard, filepath.Join(t.TempDir(), "lua"))
	f := newGoluaFields("test.Record")
	f.str["Title"] = &rec.Title
	f.num["Page"] = &rec.Page
	f.boolean["Done"] = &rec.Done
	f.list["Links"] = rec.Links
	f.methods["Twice"] = goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		n, err := checkInt(c, 0)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, rt.IntValue(int64(2*n))), nil
	}}
	f.getter["Secret"] = func(t *rt.Thread) rt.Value { return rt.StringValue("<" + rec.Secret + ">") }
	f.setter["Secret"] = func(t *rt.Thread, v rt.Value) error { rec.Secret = luaString(v); return nil }
	r.GlobalEnv().Set(rt.StringValue("OBJ"), f.push(r))

	chunk, err := r.CompileAndLoadLuaChunk("test", []byte(src), rt.TableValue(r.GlobalEnv()))
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	v, err := rt.Call1(r.MainThread(), rt.FunctionValue(chunk))
	if err != nil {
		return "", false
	}
	s, _ := v.ToString()
	return s, true
}

// TestGoluaFieldsMatchGopher runs each snippet through both helpers and
// requires the same result, the same success or failure, and the same Go
// state afterwards.
var fieldsSnippets = map[string]string{
	"read string":             `return OBJ.Title`,
	"number to string field":  `OBJ.Title = 5; return OBJ.Title`,
	"bool to string field":    `OBJ.Title = true; return '[' .. OBJ.Title .. ']'`,
	"read number":             `return tostring(OBJ.Page)`,
	"number in a URL":         `return 'page=' .. OBJ.Page`,
	"float truncates":         `OBJ.Page = 2.9; return tostring(OBJ.Page)`,
	"negative float":          `OBJ.Page = -2.9; return tostring(OBJ.Page)`,
	"numeric string":          `OBJ.Page = '7'; return tostring(OBJ.Page)`,
	"nil number is zero":      `OBJ.Page = nil; return tostring(OBJ.Page)`,
	"number arithmetic":       `OBJ.Page = OBJ.Page + 1; return tostring(OBJ.Page * 3)`,
	"truthy bool":             `OBJ.Done = 1; return tostring(OBJ.Done)`,
	"nil bool":                `OBJ.Done = true; OBJ.Done = nil; return tostring(OBJ.Done)`,
	"unknown key":             `OBJ.Nope = 1; return tostring(OBJ.Nope)`,
	"method, dot call":        `return tostring(OBJ.Twice(21))`,
	"method is a function":    `return type(OBJ.Twice)`,
	"getter and setter":       `OBJ.Secret = 'x'; return OBJ.Secret`,
	"list add and index":      `OBJ.Links.Add('a'); OBJ.Links.Add(3); return OBJ.Links.Count .. OBJ.Links[0] .. OBJ.Links[1] .. #OBJ.Links`,
	"list out of range":       `return '[' .. OBJ.Links[5] .. ']'`,
	"list put grows":          `OBJ.Links[2] = 'x'; return OBJ.Links.Count .. '|' .. OBJ.Links.Text`,
	"list float index":        `OBJ.Links.Add('a'); OBJ.Links.Add('b'); return OBJ.Links[1.7]`,
	"list text":               `OBJ.Links.Text = 'a\r\nb\n'; return OBJ.Links.Count .. OBJ.Links.Get(1)`,
	"comma text":              `OBJ.Links.CommaText = 'a,"b,c","d ""e"""'; return OBJ.Links.Count .. '|' .. OBJ.Links.CommaText`,
	"values":                  `OBJ.Links.Values['Referer'] = 'x'; OBJ.Links.Values['referer'] = 'y'; return OBJ.Links.Values['REFERER'] .. OBJ.Links.Text`,
	"values remove":           `OBJ.Links.Values['k'] = 'v'; OBJ.Links.Values['k'] = ''; return tostring(OBJ.Links.Count)`,
	"index of":                `OBJ.Links.Add('a'); OBJ.Links.Add('b'); return OBJ.Links.IndexOf('b') .. OBJ.Links.IndexOf('z')`,
	"delete":                  `OBJ.Links.Add('a'); OBJ.Links.Add('b'); OBJ.Links.Delete(0); OBJ.Links.Delete(9); return OBJ.Links.Text`,
	"reverse":                 `OBJ.Links.Add('a'); OBJ.Links.Add('b'); OBJ.Links.Reverse(); return OBJ.Links.Text`,
	"clear":                   `OBJ.Links.Add('a'); OBJ.Links.Clear(); return tostring(OBJ.Links.Count)`,
	"unknown list key":        `return tostring(OBJ.Links.Nope)`,
	"list add a table fails":  `OBJ.Links.Add({})`,
	"method with a table":     `return OBJ.Twice({})`,
	"loop over a list":        `for i = 0, 2 do OBJ.Links.Add('p' .. i) end; local s = '' for i = 0, OBJ.Links.Count - 1 do s = s .. OBJ.Links[i] end return s`,
	"count in a URL":          `OBJ.Links.Add('a'); return 'n=' .. OBJ.Links.Count`,
	"index of in arithmetic":  `OBJ.Links.Add('a'); return tostring(OBJ.Links.IndexOf('a') + 1)`,
	"page number from string": `OBJ.Page = tonumber('12'); return 'p' .. OBJ.Page`,
}

// fieldsExpect is what a fieldsSnippets entry must return and leave behind in
// the bound record. The values were taken from the gopher-lua bindings the
// golua ones replaced, so modules see no difference.
type fieldsExpect struct {
	want          string
	fails         bool
	title, secret string
	page          int
	done          bool
	links         []string
}

var fieldsWant = map[string]fieldsExpect{
	"bool to string field":    {want: "[]", fails: false, title: "", page: 1, done: false, secret: "", links: nil},
	"clear":                   {want: "0", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"comma text":              {want: "3|a,\"b,c\",\"d \"\"e\"\"\"", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a", "b,c", "d \"e\""}},
	"count in a URL":          {want: "n=1", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a"}},
	"delete":                  {want: "b", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"b"}},
	"float truncates":         {want: "2", fails: false, title: "t", page: 2, done: false, secret: "", links: nil},
	"getter and setter":       {want: "<x>", fails: false, title: "t", page: 1, done: false, secret: "x", links: nil},
	"index of":                {want: "1-1", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a", "b"}},
	"index of in arithmetic":  {want: "1", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a"}},
	"list add a table fails":  {want: "", fails: true, title: "t", page: 1, done: false, secret: "", links: nil},
	"list add and index":      {want: "2a32", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a", "3"}},
	"list float index":        {want: "b", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a", "b"}},
	"list out of range":       {want: "[]", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"list put grows":          {want: "3|\r\n\r\nx", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"", "", "x"}},
	"list text":               {want: "2b", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"a", "b"}},
	"loop over a list":        {want: "p0p1p2", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"p0", "p1", "p2"}},
	"method is a function":    {want: "function", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"method with a table":     {want: "", fails: true, title: "t", page: 1, done: false, secret: "", links: nil},
	"method, dot call":        {want: "42", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"negative float":          {want: "-2", fails: false, title: "t", page: -2, done: false, secret: "", links: nil},
	"nil bool":                {want: "false", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"nil number is zero":      {want: "0", fails: false, title: "t", page: 0, done: false, secret: "", links: nil},
	"number arithmetic":       {want: "6", fails: false, title: "t", page: 2, done: false, secret: "", links: nil},
	"number in a URL":         {want: "page=1", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"number to string field":  {want: "5", fails: false, title: "5", page: 1, done: false, secret: "", links: nil},
	"numeric string":          {want: "7", fails: false, title: "t", page: 7, done: false, secret: "", links: nil},
	"page number from string": {want: "p12", fails: false, title: "t", page: 12, done: false, secret: "", links: nil},
	"read number":             {want: "1", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"read string":             {want: "t", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"reverse":                 {want: "b\r\na", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"b", "a"}},
	"truthy bool":             {want: "true", fails: false, title: "t", page: 1, done: true, secret: "", links: nil},
	"unknown key":             {want: "nil", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"unknown list key":        {want: "nil", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
	"values":                  {want: "yreferer=y", fails: false, title: "t", page: 1, done: false, secret: "", links: []string{"referer=y"}},
	"values remove":           {want: "0", fails: false, title: "t", page: 1, done: false, secret: "", links: nil},
}

func TestFields(t *testing.T) {
	for name, src := range fieldsSnippets {
		t.Run(name, func(t *testing.T) {
			w := fieldsWant[name]
			rec := newRecord()
			got, ok := runGoluaFields(t, rec, src)
			if ok == w.fails {
				t.Fatalf("ok=%v %q, want it to fail=%v", ok, got, w.fails)
			}
			if got != w.want {
				t.Errorf("got %q, want %q", got, w.want)
			}
			if rec.Title != w.title || rec.Page != w.page || rec.Done != w.done || rec.Secret != w.secret ||
				!reflect.DeepEqual(rec.Links.All(), w.links) {
				t.Errorf("record %+v %q, want title %q page %d done %v secret %q links %q",
					rec, rec.Links.All(), w.title, w.page, w.done, w.secret, w.links)
			}
		})
	}
}

func TestGoluaFieldsMethodIsStable(t *testing.T) {
	got, ok := runGoluaFields(t, newRecord(), `return tostring(OBJ.Twice == OBJ.Twice)`)
	if !ok || got != "true" {
		t.Errorf("reading a method twice should give the same function, got %q", got)
	}
}
