package scraper

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"

	lua "github.com/yuin/gopher-lua"
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

// imagepuzzleLoader registers fmd.imagepuzzle.
func imagepuzzleLoader(L *lua.LState) int {
	create := func(L *lua.LState) int {
		L.Push(pushPuzzle(L, NewImagePuzzle(L.CheckInt(1), L.CheckInt(2))))
		return 1
	}
	L.Push(L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
		"Create": create,
		"New":    create,
	}))
	return 1
}

func registerImagePuzzle(L *lua.LState) {
	mt := L.NewTypeMetatable(puzzleTypeName)
	L.SetField(mt, "__index", L.NewFunction(puzzleIndex))
	L.SetField(mt, "__newindex", L.NewFunction(puzzleNewIndex))

	// Matrix is an indexed property, so it needs an object of its own to carry
	// the element accessors.
	mmt := L.NewTypeMetatable(matrixTypeName)
	L.SetField(mmt, "__index", L.NewFunction(func(L *lua.LState) int {
		p := L.CheckUserData(1).Value.(*ImagePuzzle)
		i := L.CheckInt(2)
		if i < 0 || i >= len(p.Matrix) {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(lua.LNumber(p.Matrix[i]))
		return 1
	}))
	L.SetField(mmt, "__newindex", L.NewFunction(func(L *lua.LState) int {
		p := L.CheckUserData(1).Value.(*ImagePuzzle)
		i := L.CheckInt(2)
		if i < 0 || i >= len(p.Matrix) {
			L.RaiseError("imagepuzzle: Matrix[%d] is outside a %dx%d grid",
				i, p.HorBlock, p.VerBlock)
			return 0
		}
		p.Matrix[i] = L.CheckInt(3)
		return 0
	}))
	L.SetField(mmt, "__len", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LNumber(len(L.CheckUserData(1).Value.(*ImagePuzzle).Matrix)))
		return 1
	}))
}

func pushPuzzle(L *lua.LState, p *ImagePuzzle) lua.LValue {
	ud := L.NewUserData()
	ud.Value = p
	L.SetMetatable(ud, L.GetTypeMetatable(puzzleTypeName))
	return ud
}

func puzzleIndex(L *lua.LState) int {
	p := L.CheckUserData(1).Value.(*ImagePuzzle)
	switch L.CheckString(2) {
	case "Matrix":
		ud := L.NewUserData()
		ud.Value = p
		L.SetMetatable(ud, L.GetTypeMetatable(matrixTypeName))
		L.Push(ud)
	case "HorBlock":
		L.Push(lua.LNumber(p.HorBlock))
	case "VerBlock":
		L.Push(lua.LNumber(p.VerBlock))
	case "Multiply":
		L.Push(lua.LNumber(p.Multiply))
	case "DeScramble":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			// Modules call DeScramble(HTTP.Document, HTTP.Document) to rewrite
			// the response in place, so input and output are often the same.
			in, out := documentArg(L, 1), documentArg(L, 2)
			if in == nil || out == nil {
				L.RaiseError("imagepuzzle: DeScramble expects two streams")
				return 0
			}
			result, err := p.DeScramble(in.data)
			if err != nil {
				L.RaiseError("%s", err.Error())
				return 0
			}
			out.data = result
			return 0
		}))
	default:
		L.Push(lua.LNil)
	}
	return 1
}

func puzzleNewIndex(L *lua.LState) int {
	p := L.CheckUserData(1).Value.(*ImagePuzzle)
	if L.CheckString(2) == "Multiply" {
		p.Multiply = L.CheckInt(3)
	}
	return 0
}
