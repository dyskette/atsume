package scraper

import (
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

// The MangaFox watermark remover.
//
// One site stamps a banner across the bottom of every page. Upstream ships a
// directory of template images of that banner and matches each page against
// them; a page that matches has the banner's height cut off the bottom. It is
// not inpainting — the banner sits below the artwork rather than over it.
//
// This is a reimplementation of that behaviour from its observable contract,
// not a translation of upstream's code. The templates themselves are read
// from the module checkout at runtime, like every other upstream asset.
const (
	// watermarkMinPSNR is how close a page's bottom strip must be to a
	// template before the strip is treated as the banner. Upstream's value.
	// Too low and artwork is amputated; too high and the banner survives.
	watermarkMinPSNR = 9.0
	// watermarkWhiteBorder is how many rows at the top of the strip must be
	// entirely white. The banner is separated from the artwork by a white
	// gap, so a strip whose first rows contain ink is artwork and is not
	// considered however well it scores.
	watermarkWhiteBorder = 4
)

// oneBit is a greyscale image reduced to black and white by Otsu's method.
type oneBit struct {
	w, h int
	bits []byte // one byte per pixel, 0 or 255
}

// otsu picks the threshold that best separates the histogram into two classes
// and applies it in place.
func otsu(b []byte) {
	if len(b) == 0 {
		return
	}
	var histogram [256]int
	for _, v := range b {
		histogram[v]++
	}
	n := float64(len(b))

	var mean float64
	for i, count := range histogram {
		mean += float64(i) * float64(count) / n
	}

	var threshold int
	var best, weight, moment float64
	for i, count := range histogram {
		weight += float64(count) / n
		moment += float64(i) * float64(count) / n
		if weight == 0 || weight == 1 {
			continue
		}
		between := mean*weight - moment
		between = between * between / (weight * (1 - weight))
		if between > best {
			best, threshold = between, i
		}
	}

	for i, v := range b {
		if int(v) > threshold {
			b[i] = 255
		} else {
			b[i] = 0
		}
	}
}

// binarize reduces a rectangle of an image to black and white.
//
// A rectangle wider than the image is white-padded with the image centred in
// it, so a page narrower than a template can still be compared against it.
func binarize(img image.Image, rect image.Rectangle) oneBit {
	out := oneBit{w: rect.Dx(), h: rect.Dy()}
	out.bits = make([]byte, out.w*out.h)
	for i := range out.bits {
		out.bits[i] = 255
	}

	b := img.Bounds()
	pad := 0
	if rect.Dx() > b.Dx() {
		pad = (rect.Dx() - b.Dx()) / 2
	}
	for y := 0; y < out.h; y++ {
		sy := b.Min.Y + rect.Min.Y + y
		if sy >= b.Max.Y {
			break
		}
		for x := 0; x+pad < out.w; x++ {
			sx := b.Min.X + rect.Min.X + x
			if sx >= b.Max.X {
				break
			}
			r, g, bl, _ := img.At(sx, sy).RGBA()
			// Rec. 601 luma on 16-bit channels, taken down to 8.
			gray := (19595*r + 38470*g + 7471*bl + 1<<15) >> 24
			out.bits[y*out.w+x+pad] = byte(gray)
		}
	}
	otsu(out.bits)
	return out
}

// psnr measures how alike two equally sized images are, in decibels.
func psnr(a, b oneBit) float64 {
	if len(a.bits) != len(b.bits) || len(a.bits) == 0 {
		return 0
	}
	var mse float64
	for i := range a.bits {
		d := float64(int(b.bits[i]) - int(a.bits[i]))
		mse += d * d
	}
	mse /= float64(len(a.bits))
	if math.Sqrt(mse) < 0.0001 {
		return 1e6
	}
	return 10 * math.Log10(255*255/mse)
}

// watermarkTemplates is the set loaded from one directory.
type watermarkTemplates struct {
	dir   string
	items []oneBit
}

// topWhite reports whether the first rows of a strip are entirely white,
// which is what separates the banner from the artwork above it.
func (t oneBit) topWhite(rows int) bool {
	if rows <= 0 {
		return true
	}
	limit := rows * t.w
	if limit > len(t.bits) {
		limit = len(t.bits)
	}
	for _, v := range t.bits[:limit] {
		if v == 0 {
			return false
		}
	}
	return true
}

var (
	watermarkMu    sync.Mutex
	watermarkCache = map[string]*watermarkTemplates{}
)

// loadWatermarkTemplates reads every image in a directory, once per directory
// for the life of the process. Modules load them from inside Init(), which
// runs for every scrape.
func loadWatermarkTemplates(dir string) (*watermarkTemplates, error) {
	// Upstream modules are written for Windows and join these paths with
	// backslashes.
	dir = filepath.Clean(strings.ReplaceAll(dir, `\`, "/"))

	watermarkMu.Lock()
	defer watermarkMu.Unlock()
	if t, ok := watermarkCache[dir]; ok {
		return t, nil
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	// Order decides nothing but which of two equally good matches wins; fix
	// it anyway so a run is reproducible.
	sort.Strings(names)

	t := &watermarkTemplates{dir: dir}
	for _, name := range names {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			continue // not an image; upstream skips these too
		}
		t.items = append(t.items, binarize(img, image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy())))
	}
	watermarkCache[dir] = t
	return t, nil
}

// match finds the template that best fits the bottom of an image, and how
// well it fits.
//
// Only the bottom strip is ever considered: the banner is stamped there, and
// comparing anywhere else would mean matching artwork against a banner.
func (t *watermarkTemplates) match(img image.Image) (best int, score float64) {
	best = -1
	b := img.Bounds()
	for i, tpl := range t.items {
		if b.Dy() < tpl.h {
			continue
		}
		left := 0
		if b.Dx() > tpl.w {
			left = (b.Dx() - tpl.w) / 2
		}
		strip := binarize(img, image.Rect(left, b.Dy()-tpl.h, left+tpl.w, b.Dy()))
		if !strip.topWhite(watermarkWhiteBorder) {
			continue
		}
		if s := psnr(strip, tpl); s > score {
			best, score = i, s
		}
	}
	return best, score
}

// remove cuts the banner off the bottom of an image file, reporting whether
// it found one.
//
// The file is rewritten in place, or under a new extension when asPNG asks
// for one, which is what the calling module expects.
func (t *watermarkTemplates) remove(path string, asPNG bool) (bool, error) {
	if len(t.items) == 0 {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	img, format, err := image.Decode(f)
	f.Close()
	if err != nil {
		return false, err
	}

	best, score := t.match(img)
	if best < 0 || score < watermarkMinPSNR {
		return false, nil
	}

	b := img.Bounds()
	keep := image.Rect(b.Min.X, b.Min.Y, b.Max.X, b.Max.Y-t.items[best].h)
	if keep.Dy() <= 0 {
		// The whole page matched. Something is wrong with the template, and
		// writing an empty image would be worse than leaving the page alone.
		return false, nil
	}
	// Cropping through SubImage keeps the concrete type, so a greyscale JPEG
	// stays greyscale rather than trebling in size as colour.
	sub, ok := img.(interface {
		SubImage(image.Rectangle) image.Image
	})
	if !ok {
		return false, fmt.Errorf("cannot crop a %T", img)
	}
	cropped := sub.SubImage(keep)

	out := path
	if asPNG {
		out = strings.TrimSuffix(path, filepath.Ext(path)) + ".png"
	}
	w, err := os.Create(out)
	if err != nil {
		return false, err
	}
	if asPNG || format == "png" {
		err = png.Encode(w, cropped)
	} else {
		err = jpeg.Encode(w, cropped, &jpeg.Options{Quality: jpegQuality})
	}
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return false, err
	}
	if out != path {
		// The module asked for a different format; the original is what it
		// asked to replace.
		os.Remove(path)
	}
	return true, nil
}

// mangafoxwatermarkLoader exposes the remover to Lua.
//
// The template set belongs to the library instance rather than the process:
// a module loads it inside Init(), and each scrape runs its own Init().
func mangafoxwatermarkLoader(L *lua.LState) int {
	var held *watermarkTemplates
	t := L.NewTable()
	L.SetFuncs(t, map[string]lua.LGFunction{
		// LoadTemplate(directory) returns how many templates were read.
		"LoadTemplate": func(L *lua.LState) int {
			loaded, err := loadWatermarkTemplates(L.CheckString(1))
			if err != nil {
				// Upstream returns zero for a directory it cannot read, and
				// the calling module carries on without watermark removal.
				slog.Debug("mangafox templates", "err", err)
				L.Push(lua.LNumber(0))
				return 1
			}
			held = loaded
			L.Push(lua.LNumber(len(loaded.items)))
			return 1
		},
		// RemoveWatermark(filename, asPNG) reports whether one was found.
		"RemoveWatermark": func(L *lua.LState) int {
			path := L.CheckString(1)
			asPNG := L.ToBool(2)
			if held == nil {
				L.Push(lua.LFalse)
				return 1
			}
			removed, err := held.remove(path, asPNG)
			if err != nil {
				L.RaiseError("fmd.mangafoxwatermark.RemoveWatermark: %v", err)
				return 0
			}
			L.Push(lua.LBool(removed))
			return 1
		},
	})
	L.Push(t)
	return 1
}
