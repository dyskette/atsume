package scraper

import "testing"

// TestMaybeFillHostNetworkPath covers the address shape that broke FanFox.
//
// A page link of the form //cdn.example/p.jpg is an ordinary URL that simply
// borrows the page's scheme. Go's client refuses it outright, so a module
// handing one back failed with "unsupported protocol scheme".
func TestMaybeFillHostNetworkPath(t *testing.T) {
	cases := []struct{ root, in, want string }{
		{"https://fanfox.net", "//zjcdn.example/p.jpg", "https://zjcdn.example/p.jpg"},
		{"http://plain.example", "//cdn.example/p.jpg", "http://cdn.example/p.jpg"},
		// Everything else keeps working as before.
		{"https://fanfox.net", "/manga/x/", "https://fanfox.net/manga/x/"},
		{"https://fanfox.net", "https://other.example/p.jpg", "https://other.example/p.jpg"},
		{"https://fanfox.net/directory/", "2.html", "https://fanfox.net/directory/2.html"},
	}
	for _, c := range cases {
		if got := MaybeFillHost(c.root, c.in); got != c.want {
			t.Errorf("MaybeFillHost(%q, %q) = %q, want %q", c.root, c.in, got, c.want)
		}
	}
}

// TestNormaliseLink covers the shape FMD2 stores links in.
//
// MangaDex hands back a bare chapter identifier and then builds its page
// request by concatenation: API_URL .. '/at-home/server' .. URL. Upstream's
// RemoveHostFromURL ends with `ipath := '/' + iurl`, so the module gets a
// leading slash and the address resolves. Without it the two run together,
// the request 404s, and the download fails with "module returned no pages".
func TestNormaliseLink(t *testing.T) {
	cases := []struct{ in, want string }{
		// The case that broke: a bare identifier.
		{"caf13eb0-6bca-4abb-a243-80831ae7fda6", "/caf13eb0-6bca-4abb-a243-80831ae7fda6"},
		{"manga/foo", "/manga/foo"},
		// Already in shape, and left alone.
		{"/manga/foo", "/manga/foo"},
		{"/title/da0ccc81", "/title/da0ccc81"},
		// Absolute addresses stay absolute: every fetch goes through
		// MaybeFillHost, which passes them through, and rewriting them would
		// change links already stored against followed series.
		{"https://mangatoon.mobi/en/x?content_id=1", "https://mangatoon.mobi/en/x?content_id=1"},
		{"http://example.invalid/a", "http://example.invalid/a"},
		// Nothing in, nothing out.
		{"", ""},
		{"   ", ""},
		{"  /manga/foo  ", "/manga/foo"},
	}
	for _, c := range cases {
		if got := NormaliseLink(c.in); got != c.want {
			t.Errorf("NormaliseLink(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
