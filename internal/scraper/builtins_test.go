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
