package scraper

import (
	"io"
	"path/filepath"
	"reflect"
	"testing"

	rt "github.com/arnodel/golua/runtime"
	lua "github.com/yuin/gopher-lua"
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

// runGopherFields runs src against a record bound with the gopher-lua helper.
func runGopherFields(t *testing.T, rec *record, src string) (string, bool) {
	t.Helper()
	L := lua.NewState()
	defer L.Close()
	registerStrings(L)
	registerValues(L)
	f := newFields("test.Record")
	f.str["Title"] = &rec.Title
	f.num["Page"] = &rec.Page
	f.boolean["Done"] = &rec.Done
	f.list["Links"] = rec.Links
	f.methods["Twice"] = func(L *lua.LState) int {
		L.Push(lua.LNumber(2 * L.CheckInt(1)))
		return 1
	}
	f.getter["Secret"] = func(L *lua.LState) lua.LValue { return lua.LString("<" + rec.Secret + ">") }
	f.setter["Secret"] = func(L *lua.LState, v lua.LValue) { rec.Secret = lua.LVAsString(v) }
	L.SetGlobal("OBJ", f.push(L))
	if err := L.DoString(src); err != nil {
		return "", false
	}
	return L.Get(-1).String(), true
}

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
func TestGoluaFieldsMatchGopher(t *testing.T) {
	snippets := map[string]string{
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
	for name, src := range snippets {
		t.Run(name, func(t *testing.T) {
			g, n := newRecord(), newRecord()
			gotG, okG := runGopherFields(t, g, src)
			gotN, okN := runGoluaFields(t, n, src)
			if okG != okN {
				t.Fatalf("gopher-lua ok=%v, golua ok=%v", okG, okN)
			}
			if gotG != gotN {
				t.Errorf("gopher-lua %q, golua %q", gotG, gotN)
			}
			if !reflect.DeepEqual(g, n) {
				t.Errorf("Go state differs:\ngopher-lua %+v %v\n     golua %+v %v", g, g.Links.All(), n, n.Links.All())
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
