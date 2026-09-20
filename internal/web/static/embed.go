// Package static serves the front-end assets, embedded so that a release is a
// single binary.
package static

import "embed"

// FS holds the stylesheet and the htmx scripts.
//
//go:embed *.js *.css
var FS embed.FS
