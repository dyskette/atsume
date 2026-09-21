package scraper

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// bannerTemplate draws something shaped like the thing being matched: a white
// gap at the top, then dark marks across the rest.
func bannerTemplate(w, h int) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: 255}), image.Point{}, draw.Src)
	for y := watermarkWhiteBorder + 2; y < h-2; y++ {
		for x := 4; x < w-4; x++ {
			if (x/7+y/5)%3 != 0 {
				img.SetGray(x, y, color.Gray{Y: 20})
			}
		}
	}
	return img
}

// pageWith draws artwork and optionally stamps the banner across the bottom.
func pageWith(w, h int, banner image.Image) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, w, h))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.Gray{Y: 255}), image.Point{}, draw.Src)
	// Artwork: something with ink in it, unlike the banner's white gap.
	art := h
	if banner != nil {
		art = h - banner.Bounds().Dy()
	}
	for y := 0; y < art; y++ {
		for x := 0; x < w; x++ {
			if (x+y)%11 < 4 {
				img.SetGray(x, y, color.Gray{Y: 40})
			}
		}
	}
	if banner != nil {
		b := banner.Bounds()
		at := image.Rect((w-b.Dx())/2, h-b.Dy(), (w-b.Dx())/2+b.Dx(), h)
		draw.Draw(img, at, banner, b.Min, draw.Src)
	}
	return img
}

func writeJPEG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: jpegQuality}); err != nil {
		t.Fatal(err)
	}
	f.Close()
}

// templateDir writes a template set and returns its directory.
func templateDir(t *testing.T, banner image.Image) string {
	t.Helper()
	dir := t.TempDir()
	f, err := os.Create(filepath.Join(dir, "banner.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, banner); err != nil {
		t.Fatal(err)
	}
	f.Close()
	// Something that is not an image, which upstream skips silently.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestWatermarkRemoved covers the whole point: a page carrying the banner
// loses exactly the banner.
func TestWatermarkRemoved(t *testing.T) {
	banner := bannerTemplate(728, 60)
	tpl, err := loadWatermarkTemplates(templateDir(t, banner))
	if err != nil {
		t.Fatal(err)
	}
	if len(tpl.items) != 1 {
		t.Fatalf("loaded %d templates, want 1 (the text file is not one)", len(tpl.items))
	}

	path := filepath.Join(t.TempDir(), "page.jpg")
	writeJPEG(t, path, pageWith(728, 500, banner))

	removed, err := tpl.remove(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("the banner was not recognised")
	}

	f, _ := os.Open(path)
	out, format, err := image.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if format != "jpeg" {
		t.Errorf("format = %q, want the original kept", format)
	}
	if got := out.Bounds().Dy(); got != 440 {
		t.Errorf("height = %d, want 440: the banner's 60 rows and nothing else", got)
	}
	if got := out.Bounds().Dx(); got != 728 {
		t.Errorf("width = %d, want it untouched", got)
	}
}

// TestWatermarkLeavesACleanPage is the failure that matters. Cutting the
// bottom off a page that never had a banner amputates the artwork, and the
// reader has no way to know it happened.
func TestWatermarkLeavesACleanPage(t *testing.T) {
	banner := bannerTemplate(728, 60)
	tpl, err := loadWatermarkTemplates(templateDir(t, banner))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "page.jpg")
	writeJPEG(t, path, pageWith(728, 500, nil))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	removed, err := tpl.remove(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("a page with no banner was cropped")
	}
	after, _ := os.ReadFile(path)
	if len(before) != len(after) {
		t.Error("a page with no banner was rewritten")
	}
}

// TestWatermarkSaveAsPNG covers the option the module exposes, and the rename
// it causes.
func TestWatermarkSaveAsPNG(t *testing.T) {
	banner := bannerTemplate(728, 60)
	tpl, err := loadWatermarkTemplates(templateDir(t, banner))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "page.jpg")
	writeJPEG(t, path, pageWith(728, 500, banner))

	removed, err := tpl.remove(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if !removed {
		t.Fatal("the banner was not recognised")
	}
	if _, err := os.Stat(path); err == nil {
		t.Error("the original should not survive alongside the replacement")
	}
	out := filepath.Join(dir, "page.png")
	f, err := os.Open(out)
	if err != nil {
		t.Fatalf("no png was written: %v", err)
	}
	img, format, err := image.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if format != "png" {
		t.Errorf("format = %q, want png", format)
	}
	if got := img.Bounds().Dy(); got != 440 {
		t.Errorf("height = %d, want 440", got)
	}
}

// TestWatermarkNeedsAWhiteGap covers the check that stops artwork being
// mistaken for a banner: the real banner sits below a white gap.
func TestWatermarkNeedsAWhiteGap(t *testing.T) {
	banner := bannerTemplate(728, 60)
	tpl, err := loadWatermarkTemplates(templateDir(t, banner))
	if err != nil {
		t.Fatal(err)
	}

	// The banner, but with ink where the gap should be.
	inked := bannerTemplate(728, 60)
	for x := 0; x < 728; x++ {
		inked.SetGray(x, 1, color.Gray{Y: 0})
	}
	page := pageWith(728, 500, inked)

	path := filepath.Join(t.TempDir(), "page.jpg")
	writeJPEG(t, path, page)
	removed, err := tpl.remove(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if removed {
		t.Error("a strip with ink in its top rows is artwork, not the banner")
	}
}

// TestWatermarkThroughLua covers the binding as the module calls it, with the
// Windows-style path upstream writes.
func TestWatermarkThroughLua(t *testing.T) {
	banner := bannerTemplate(728, 60)
	tpls := templateDir(t, banner)

	pages := t.TempDir()
	path := filepath.Join(pages, "page.jpg")
	writeJPEG(t, path, pageWith(728, 500, banner))

	dir := luaDir(t)
	src := fmt.Sprintf(`
local TEMPLATES = %q

function Init()
	local m = NewWebsiteModule()
	m.ID                 = '1'
	m.Name               = 'Watermarked'
	m.RootURL            = 'https://example.invalid'
	m.OnAfterImageSaved  = 'AfterImageSaved'
	m.AddOptionCheckBox('mf_saveaspng', 'Save as PNG', false)
	COUNT = require('fmd.mangafoxwatermark').LoadTemplate(TEMPLATES)
end

function AfterImageSaved()
	REMOVED = require('fmd.mangafoxwatermark').RemoveWatermark(FILENAME, MODULE.GetOption('mf_saveaspng'))
	return true
end
`, tpls+`\\`)
	modPath := filepath.Join(t.TempDir(), "Watermarked.lua")
	if err := os.WriteFile(modPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	// The path is spelled with a trailing backslash, the way upstream writes
	// it: these modules are written for Windows.
	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), modPath, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if got := r.L.GetGlobal("COUNT").String(); got != "1" {
		t.Fatalf("LoadTemplate returned %s, want 1", got)
	}
	if err := r.AfterImageSaved(path); err != nil {
		t.Fatal(err)
	}
	if got := r.L.GetGlobal("REMOVED").String(); got != "true" {
		t.Errorf("RemoveWatermark returned %s", got)
	}
	f, _ := os.Open(path)
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds().Dy(); got != 440 {
		t.Errorf("height = %d, want the banner gone", got)
	}
}
