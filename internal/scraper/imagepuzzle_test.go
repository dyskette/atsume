package scraper

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

// gridImage builds an image whose every tile is a distinct flat colour, so a
// misplaced tile is detectable by comparing pixels.
func gridImage(hor, ver, tileW, tileH int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, hor*tileW, ver*tileH))
	for i := 0; i < hor*ver; i++ {
		c := color.RGBA{R: uint8(17 * (i + 1)), G: uint8(255 - 13*i), B: uint8(7 * i), A: 255}
		x0, y0 := (i%hor)*tileW, (i/hor)*tileH
		for y := y0; y < y0+tileH; y++ {
			for x := x0; x < x0+tileW; x++ {
				img.Set(x, y, c)
			}
		}
	}
	return img
}

// scramble produces the image a site would serve: DeScramble moves source tile
// i to destination matrix[i], so the scrambled tile i must hold what belongs at
// matrix[i].
func scramble(src *image.RGBA, hor, ver, tileW, tileH int, matrix []int) *image.RGBA {
	out := image.NewRGBA(src.Bounds())
	for i := 0; i < hor*ver; i++ {
		sx, sy := (matrix[i]%hor)*tileW, (matrix[i]/hor)*tileH
		dx, dy := (i%hor)*tileW, (i/hor)*tileH
		for y := 0; y < tileH; y++ {
			for x := 0; x < tileW; x++ {
				out.Set(dx+x, dy+y, src.At(sx+x, sy+y))
			}
		}
	}
	return out
}

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func decodeRGBA(t *testing.T, b []byte) *image.RGBA {
	t.Helper()
	img, _, err := image.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out := image.NewRGBA(img.Bounds())
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			out.Set(x, y, img.At(x, y))
		}
	}
	return out
}

// identical reports the first differing pixel, or -1,-1 when the images match.
func identical(a, b *image.RGBA) (int, int) {
	if a.Bounds() != b.Bounds() {
		return 0, 0
	}
	for y := a.Bounds().Min.Y; y < a.Bounds().Max.Y; y++ {
		for x := a.Bounds().Min.X; x < a.Bounds().Max.X; x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				return x, y
			}
		}
	}
	return -1, -1
}

// TestDeScrambleRoundTrip is the core proof: a scrambled image reassembles into
// the original, pixel for pixel, for a range of grids and permutations.
func TestDeScrambleRoundTrip(t *testing.T) {
	cases := []struct{ hor, ver, tileW, tileH int }{
		{2, 2, 8, 8},
		{3, 3, 10, 10},
		{4, 4, 16, 16},
		{5, 3, 12, 20},
		{1, 1, 9, 9},
	}
	rng := rand.New(rand.NewSource(1))

	for _, c := range cases {
		t.Run("", func(t *testing.T) {
			n := c.hor * c.ver
			matrix := rng.Perm(n)

			original := gridImage(c.hor, c.ver, c.tileW, c.tileH)
			scrambled := scramble(original, c.hor, c.ver, c.tileW, c.tileH, matrix)

			p := NewImagePuzzle(c.hor, c.ver)
			copy(p.Matrix, matrix)

			out, err := p.DeScramble(encodePNG(t, scrambled))
			if err != nil {
				t.Fatal(err)
			}
			if x, y := identical(decodeRGBA(t, out), original); x >= 0 {
				t.Errorf("%dx%d grid: pixel (%d,%d) differs", c.hor, c.ver, x, y)
			}
		})
	}
}

// TestDeScrambleIdentity checks that the default matrix is a no-op.
func TestDeScrambleIdentity(t *testing.T) {
	original := gridImage(3, 3, 8, 8)
	p := NewImagePuzzle(3, 3)

	out, err := p.DeScramble(encodePNG(t, original))
	if err != nil {
		t.Fatal(err)
	}
	if x, y := identical(decodeRGBA(t, out), original); x >= 0 {
		t.Errorf("identity matrix changed pixel (%d,%d)", x, y)
	}
}

func TestDeScrambleErrors(t *testing.T) {
	valid := encodePNG(t, gridImage(2, 2, 4, 4))

	t.Run("matrix out of range", func(t *testing.T) {
		p := NewImagePuzzle(2, 2)
		p.Matrix[0] = 99
		if _, err := p.DeScramble(valid); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("undecodable input", func(t *testing.T) {
		p := NewImagePuzzle(2, 2)
		if _, err := p.DeScramble([]byte("not an image")); err == nil {
			t.Fatal("expected an error")
		}
	})
	t.Run("zero grid", func(t *testing.T) {
		p := NewImagePuzzle(0, 0)
		if _, err := p.DeScramble(valid); err == nil {
			t.Fatal("expected an error")
		}
	})
}

// TestDeScrambleFormat pins the output format rules taken from upstream.
func TestDeScrambleFormat(t *testing.T) {
	img := gridImage(2, 2, 8, 8)

	t.Run("png stays png", func(t *testing.T) {
		out, err := NewImagePuzzle(2, 2).DeScramble(encodePNG(t, img))
		if err != nil {
			t.Fatal(err)
		}
		if _, format, _ := image.Decode(bytes.NewReader(out)); format != "png" {
			t.Errorf("format = %q, want png", format)
		}
	})

	t.Run("jpeg stays jpeg", func(t *testing.T) {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, nil); err != nil {
			t.Fatal(err)
		}
		out, err := NewImagePuzzle(2, 2).DeScramble(buf.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if _, format, _ := image.Decode(bytes.NewReader(out)); format != "jpeg" {
			t.Errorf("format = %q, want jpeg", format)
		}
	})
}

// TestDeScrambleUnevenGrid covers an image whose dimensions are not a multiple
// of the grid: the leftover margin must stay opaque white rather than read out
// of bounds.
func TestDeScrambleUnevenGrid(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 33, 33))
	for i := range img.Pix {
		img.Pix[i] = 128
	}
	out, err := NewImagePuzzle(4, 4).DeScramble(encodePNG(t, img))
	if err != nil {
		t.Fatal(err)
	}
	got := decodeRGBA(t, out)
	if got.Bounds().Dx() != 33 || got.Bounds().Dy() != 33 {
		t.Fatalf("size = %v", got.Bounds())
	}
	// 33/4 = 8, so the last row and column of pixels are uncovered.
	if c := got.RGBAAt(32, 32); c != (color.RGBA{255, 255, 255, 255}) {
		t.Errorf("uncovered margin = %v, want opaque white", c)
	}
}

// TestDeScrambleMultiply covers the tile-size rounding some sites require.
func TestDeScrambleMultiply(t *testing.T) {
	p := NewImagePuzzle(4, 4)
	p.Multiply = 8
	// 100 / (4*8) = 3, times 8 = 24.
	if w, h := p.blockSize(100, 100); w != 24 || h != 24 {
		t.Errorf("blockSize = %dx%d, want 24x24", w, h)
	}
	p.Multiply = 1
	if w, h := p.blockSize(100, 100); w != 25 || h != 25 {
		t.Errorf("blockSize = %dx%d, want 25x25", w, h)
	}
}

// TestImagePuzzleFromLua exercises the binding exactly as WolfManga and MangaGo
// use it, including rewriting HTTP.Document in place.
func TestImagePuzzleFromLua(t *testing.T) {
	dir := luaDir(t)

	hor, ver, tile := 3, 3, 12
	matrix := []int{4, 0, 8, 2, 6, 1, 7, 3, 5}
	original := gridImage(hor, ver, tile, tile)
	scrambled := encodePNG(t, scramble(original, hor, ver, tile, tile, matrix))

	path := filepath.Join(t.TempDir(), "puzzle.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa'
	m.Name            = 'PuzzleTest'
	m.RootURL         = 'https://example.invalid'
	m.OnGetPageNumber = 'GetPageNumber'
end

MATRIX = {4, 0, 8, 2, 6, 1, 7, 3, 5}

function GetPageNumber()
	local puzzle = require 'fmd.imagepuzzle'.Create(3, 3)
	for i = 0, 8 do
		puzzle.Matrix[i] = MATRIX[i + 1]
	end
	TASK.PageLinks.Add(tostring(puzzle.HorBlock) .. 'x' .. tostring(puzzle.VerBlock))
	TASK.PageLinks.Add(tostring(puzzle.Matrix[2]))
	puzzle.DeScramble(HTTP.Document, HTTP.Document)
	TASK.PageLinks.Add(tostring(#HTTP.Document))
	return true
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// Seed the response the module descrambles, as a real fetch would.
	r.http.Document.Set(scrambled)

	pages, err := r.GetPageNumber("https://example.invalid/p1.png")
	if err != nil {
		t.Fatal(err)
	}
	if pages[0] != "3x3" {
		t.Errorf("HorBlock/VerBlock = %s", pages[0])
	}
	if pages[1] != "8" {
		t.Errorf("Matrix[2] = %s, want 8", pages[1])
	}

	// The descrambled bytes must have replaced the response in place.
	if x, y := identical(decodeRGBA(t, r.http.Document.Bytes()), original); x >= 0 {
		t.Errorf("pixel (%d,%d) differs after DeScramble through Lua", x, y)
	}
}

// TestImagePuzzleMatrixBounds confirms a bad index fails loudly rather than
// corrupting a neighbouring tile.
func TestImagePuzzleMatrixBounds(t *testing.T) {
	p := NewImagePuzzle(2, 2)
	if len(p.Matrix) != 4 {
		t.Fatalf("matrix has %d entries", len(p.Matrix))
	}
	for i, v := range p.Matrix {
		if v != i {
			t.Errorf("Matrix[%d] = %d, want the identity", i, v)
		}
	}
}
