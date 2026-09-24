package scraper

import (
	"errors"
	"fmt"

	rt "github.com/arnodel/golua/runtime"
)

// This file is the golua counterpart of the fmd.imagepuzzle binding in
// imagepuzzle.go. The descrambler itself, ImagePuzzle, is shared.

func goluaImagePuzzle(r *rt.Runtime) *rt.Table {
	create := goFn{2, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		hor, err := checkInt(c, 0)
		if err != nil {
			return nil, err
		}
		ver, err := checkInt(c, 1)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, pushGoluaPuzzle(t.Runtime, NewImagePuzzle(hor, ver))), nil
	}}
	return newLib(r, map[string]goFn{"Create": create, "New": create})
}

func toGoluaPuzzle(c *rt.GoCont) (*ImagePuzzle, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if p, ok := u.Value().(*ImagePuzzle); ok {
			return p, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (image puzzle expected, got %s)", c.Arg(0).TypeName())
}

func pushGoluaPuzzle(r *rt.Runtime, p *ImagePuzzle) rt.Value {
	meta := typeMeta(r, puzzleTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(puzzleIndexGolua, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toGoluaPuzzle(c)
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

func puzzleIndexGolua(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	p, err := toGoluaPuzzle(c)
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
		v = pushGoluaMatrix(t.Runtime, p)
	case "HorBlock":
		v = rt.IntValue(int64(p.HorBlock))
	case "VerBlock":
		v = rt.IntValue(int64(p.VerBlock))
	case "Multiply":
		v = rt.IntValue(int64(p.Multiply))
	case "DeScramble":
		v = goluaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			// Modules call DeScramble(HTTP.Document, HTTP.Document) to rewrite
			// the response in place, so input and output are often the same.
			in, out := goluaDocumentArg(c, 0), goluaDocumentArg(c, 1)
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

// pushGoluaMatrix exposes Matrix, an indexed property, as an object of its
// own carrying the element accessors. Indexes are 0-based, as upstream's.
func pushGoluaMatrix(r *rt.Runtime, p *ImagePuzzle) rt.Value {
	meta := typeMeta(r, matrixTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toGoluaPuzzle(c)
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
			p, err := toGoluaPuzzle(c)
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
			p, err := toGoluaPuzzle(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(p.Matrix)))), nil
		}, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(p, meta))
}
