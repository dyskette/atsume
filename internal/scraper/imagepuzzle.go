package scraper

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"

	rt "github.com/arnodel/golua/runtime"
	_ "golang.org/x/image/webp" // decode-only, for sites serving scrambled webp
)

// jpegQuality is used when re-encoding a descrambled JPEG.
//
// Descrambling always costs one generation of JPEG loss because the tiles have
// to be rearranged in pixel space. 95 keeps that loss below what a reader will
// notice; upstream leaves it at the toolkit default, which is visibly worse.
const jpegQuality = 95

// ImagePuzzle reassembles an image whose tiles a site has deliberately shuffled.
//
// The site serves the image cut into a HorBlock × VerBlock grid with the tiles
// permuted, and ships the permutation alongside it. Matrix[i] gives the
// destination index of source tile i.
type ImagePuzzle struct {
	HorBlock int
	VerBlock int

	// Multiply rounds the tile size down to a multiple of itself, which some
	// sites need because they cut on a coarser grid than the image dimensions.
	Multiply int

	// Matrix maps each source tile index to its destination index.
	Matrix []int

	// Flips optionally mirrors a tile: bit 0 horizontally, bit 1 vertically.
	// Upstream exposes no way to set this from Lua, so it is always zero there.
	Flips []int
}

// NewImagePuzzle returns an identity puzzle over a HorBlock × VerBlock grid.
func NewImagePuzzle(hor, ver int) *ImagePuzzle {
	n := hor * ver
	if n < 0 {
		n = 0
	}
	p := &ImagePuzzle{
		HorBlock: hor,
		VerBlock: ver,
		Multiply: 1,
		Matrix:   make([]int, n),
		Flips:    make([]int, n),
	}
	for i := range p.Matrix {
		p.Matrix[i] = i
	}
	return p
}

// DeScramble reassembles src and returns the re-encoded image.
//
// The output format follows upstream: PNG in, or WebP in, yields PNG out;
// anything else yields JPEG. WebP is decode-only in Go, so converting it to PNG
// is a necessity rather than a choice.
func (p *ImagePuzzle) DeScramble(src []byte) ([]byte, error) {
	if len(p.Matrix) == 0 {
		return nil, fmt.Errorf("imagepuzzle: matrix is not set")
	}
	blocks := p.HorBlock * p.VerBlock
	if p.HorBlock <= 0 || p.VerBlock <= 0 {
		return nil, fmt.Errorf("imagepuzzle: grid is %dx%d", p.HorBlock, p.VerBlock)
	}
	if len(p.Matrix) < blocks {
		return nil, fmt.Errorf("imagepuzzle: matrix has %d entries, need %d", len(p.Matrix), blocks)
	}

	decoded, format, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, fmt.Errorf("imagepuzzle: decode: %w", err)
	}

	bounds := decoded.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("imagepuzzle: image is %dx%d", width, height)
	}

	// Normalise to RGBA so tiles can be moved with a plain slice copy.
	source := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(source, source.Bounds(), decoded, bounds.Min, draw.Src)

	blockWidth, blockHeight := p.blockSize(width, height)
	if blockWidth <= 0 || blockHeight <= 0 {
		return nil, fmt.Errorf("imagepuzzle: tile size is %dx%d for a %dx%d image",
			blockWidth, blockHeight, width, height)
	}

	// Areas no tile covers stay opaque white, matching upstream. An image whose
	// dimensions are not a multiple of the grid leaves such a margin.
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	for i := range dst.Pix {
		dst.Pix[i] = 255
	}

	for i := 0; i < blocks; i++ {
		target := p.Matrix[i]
		if target < 0 || target >= blocks {
			return nil, fmt.Errorf("imagepuzzle: matrix[%d] = %d is out of range 0..%d",
				i, target, blocks-1)
		}
		dx := (target % p.HorBlock) * blockWidth
		dy := (target / p.HorBlock) * blockHeight
		sx := (i % p.HorBlock) * blockWidth
		sy := (i / p.HorBlock) * blockHeight

		p.copyTile(dst, source, sx, sy, dx, dy, blockWidth, blockHeight, p.flip(i))
	}

	var out bytes.Buffer
	switch format {
	case "png", "webp":
		err = png.Encode(&out, dst)
	default:
		err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: jpegQuality})
	}
	if err != nil {
		return nil, fmt.Errorf("imagepuzzle: encode: %w", err)
	}
	return out.Bytes(), nil
}

// blockSize computes the tile dimensions, rounding down to a multiple of
// Multiply when one is set.
func (p *ImagePuzzle) blockSize(width, height int) (int, int) {
	if p.Multiply <= 1 {
		return width / p.HorBlock, height / p.VerBlock
	}
	return width / (p.HorBlock * p.Multiply) * p.Multiply,
		height / (p.VerBlock * p.Multiply) * p.Multiply
}

func (p *ImagePuzzle) flip(i int) int {
	if i < 0 || i >= len(p.Flips) {
		return 0
	}
	return p.Flips[i]
}

// copyTile moves one tile, clipping at the image edge so a grid that does not
// divide the image evenly cannot read or write out of bounds.
func (p *ImagePuzzle) copyTile(dst, src *image.RGBA, sx, sy, dx, dy, w, h, flags int) {
	bounds := src.Bounds()
	for row := 0; row < h; row++ {
		srcRow := sy + row
		if flags&2 != 0 {
			srcRow = sy + (h - 1 - row)
		}
		dstRow := dy + row
		if srcRow < 0 || srcRow >= bounds.Dy() || dstRow < 0 || dstRow >= bounds.Dy() {
			continue
		}

		width := w
		if sx+width > bounds.Dx() {
			width = bounds.Dx() - sx
		}
		if dx+width > bounds.Dx() {
			width = bounds.Dx() - dx
		}
		if width <= 0 {
			continue
		}

		srcStart := src.PixOffset(sx, srcRow)
		dstStart := dst.PixOffset(dx, dstRow)
		if flags&1 != 0 {
			for x := 0; x < width; x++ {
				s := srcStart + (width-1-x)*4
				d := dstStart + x*4
				copy(dst.Pix[d:d+4], src.Pix[s:s+4])
			}
			continue
		}
		copy(dst.Pix[dstStart:dstStart+width*4], src.Pix[srcStart:srcStart+width*4])
	}
}

const (
	puzzleTypeName = "atsume.ImagePuzzle"
	matrixTypeName = "atsume.ImagePuzzleMatrix"
)

func imagePuzzleLib(r *rt.Runtime) *rt.Table {
	create := goFn{2, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		hor, err := checkInt(c, 0)
		if err != nil {
			return nil, err
		}
		ver, err := checkInt(c, 1)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, pushPuzzle(t.Runtime, NewImagePuzzle(hor, ver))), nil
	}}
	return newLib(r, map[string]goFn{"Create": create, "New": create})
}

func toPuzzle(c *rt.GoCont) (*ImagePuzzle, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if p, ok := u.Value().(*ImagePuzzle); ok {
			return p, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (image puzzle expected, got %s)", c.Arg(0).TypeName())
}

func pushPuzzle(r *rt.Runtime, p *ImagePuzzle) rt.Value {
	meta := typeMeta(r, puzzleTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(puzzleIndex, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toPuzzle(c)
			if err != nil {
				return nil, err
			}
			key, err := checkString(c, 1)
			if err != nil {
				return nil, err
			}
			if key == "Multiply" {
				if p.Multiply, err = checkInt(c, 2); err != nil {
					return nil, err
				}
			}
			return c.Next(), nil
		}, "__newindex", 3, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(p, meta))
}

func puzzleIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	p, err := toPuzzle(c)
	if err != nil {
		return nil, err
	}
	key, err := checkString(c, 1)
	if err != nil {
		return nil, err
	}
	var v rt.Value
	switch key {
	case "Matrix":
		v = pushMatrix(t.Runtime, p)
	case "HorBlock":
		v = rt.IntValue(int64(p.HorBlock))
	case "VerBlock":
		v = rt.IntValue(int64(p.VerBlock))
	case "Multiply":
		v = rt.IntValue(int64(p.Multiply))
	case "DeScramble":
		v = luaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			// Modules call DeScramble(HTTP.Document, HTTP.Document) to rewrite
			// the response in place, so input and output are often the same.
			in, out := documentArg(c, 0), documentArg(c, 1)
			if in == nil || out == nil {
				return rt.NilValue, errors.New("imagepuzzle: DeScramble expects two streams")
			}
			result, err := p.DeScramble(in.Bytes())
			if err != nil {
				return rt.NilValue, err
			}
			out.Set(result)
			return rt.NilValue, nil
		})
	}
	return c.PushingNext1(t.Runtime, v), nil
}

// pushMatrix exposes Matrix, an indexed property, as an object of its
// own carrying the element accessors. Indexes are 0-based, as upstream's.
func pushMatrix(r *rt.Runtime, p *ImagePuzzle) rt.Value {
	meta := typeMeta(r, matrixTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toPuzzle(c)
			if err != nil {
				return nil, err
			}
			i, err := checkInt(c, 1)
			if err != nil {
				return nil, err
			}
			if i < 0 || i >= len(p.Matrix) {
				return c.PushingNext1(t.Runtime, rt.NilValue), nil
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(p.Matrix[i]))), nil
		}, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toPuzzle(c)
			if err != nil {
				return nil, err
			}
			i, err := checkInt(c, 1)
			if err != nil {
				return nil, err
			}
			if i < 0 || i >= len(p.Matrix) {
				return nil, fmt.Errorf("imagepuzzle: Matrix[%d] is outside a %dx%d grid", i, p.HorBlock, p.VerBlock)
			}
			if p.Matrix[i], err = checkInt(c, 2); err != nil {
				return nil, err
			}
			return c.Next(), nil
		}, "__newindex", 3, false)))
		mt.Set(rt.StringValue("__len"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toPuzzle(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(p.Matrix)))), nil
		}, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(p, meta))
}
