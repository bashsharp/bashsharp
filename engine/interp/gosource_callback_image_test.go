//go:build full

package interp_test

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
// Original Go Tour image.Image callbacks: At, Bounds, ColorModel, and the
// color.Color the original At returns. The fixture is the unchanged Tour
// solution; its bytes are authenticated by SHA-256 before any mode runs.
//
// All three modes — native, interpreted and source-free-compiled — are checked
// against each other on the decoded original PNG, not merely on the output
// string: identical bounds and an identical colour at every one of the 65536
// pixels. The original method bodies run in the interpreter; the helper only
// ever carries generated transport stubs for them.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const callbackImageDigest = "d78cda6272212f8a73bd991efa502753b4ff36edcd004b21539c2111b16b547f"

// callbackImageOriginal returns the unchanged original source, refusing to run
// on any edited copy.
func callbackImageOriginal(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/gosource-callback-image/image.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != callbackImageDigest {
		t.Fatal("original fixture digest changed")
	}
	return string(raw)
}

// callbackImageDecode recovers the PNG that pic.ShowImage base64-encodes onto
// stdout, so mode agreement is checked on the decoded original image and not
// only on an opaque output string.
func callbackImageDecode(t *testing.T, out string) image.Image {
	t.Helper()
	payload, ok := strings.CutPrefix(strings.TrimRight(out, "\n"), "IMAGE:")
	if !ok {
		t.Fatalf("no original image on stdout: %q", out)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("original image is not base64: %v", err)
	}
	m, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("original image is not a PNG: %v", err)
	}
	return m
}

// callbackImageSame compares two decoded images at every pixel.
func callbackImageSame(t *testing.T, mode string, got, want image.Image) {
	t.Helper()
	if got.Bounds() != want.Bounds() {
		t.Fatalf("%s Bounds %v, native %v", mode, got.Bounds(), want.Bounds())
	}
	for y := want.Bounds().Min.Y; y < want.Bounds().Max.Y; y++ {
		for x := want.Bounds().Min.X; x < want.Bounds().Max.X; x++ {
			if got.At(x, y) != want.At(x, y) {
				t.Fatalf("%s pixel (%d,%d) %v, native %v", mode, x, y, got.At(x, y), want.At(x, y))
			}
		}
	}
}

// TestGoSourceTourImageCallback runs the original in all three modes and proves
// they produce the identical original PNG, pixel for pixel.
func TestGoSourceTourImageCallback(t *testing.T) {
	source := callbackImageOriginal(t)
	dir := callbackTourModule(t)
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := func(source, binary string) {
		cmd := exec.CommandContext(ctx, "go", "build", "-mod=readonly", "-p", "2", "-o", binary, source)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v %s", err, out)
		}
	}
	run := func(binary string) string {
		cmd := exec.CommandContext(ctx, binary)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "PATH=")
		var out, errs bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errs
		if err := cmd.Run(); err != nil {
			t.Fatalf("run %s: %v %s", binary, err, errs.String())
		}
		if errs.Len() != 0 {
			t.Fatalf("run %s: unexpected stderr %q", binary, errs.String())
		}
		return out.String()
	}
	oracle := filepath.Join(dir, "oracle")
	build(path, oracle)
	wantOut := run(oracle)
	want := callbackImageDecode(t, wantOut)

	// The original Bounds is Image{256, 256} -> image.Rect(0, 0, 256, 256).
	if got := want.Bounds(); got != image.Rect(0, 0, 256, 256) {
		t.Fatalf("original Bounds: %v", got)
	}
	// The original At is color.RGBA{c, c, 255, 255} for c = uint8(x ^ y).
	for _, p := range []image.Point{{0, 0}, {1, 0}, {255, 255}, {13, 200}} {
		c := uint8(p.X ^ p.Y)
		wr, wg, wb, wa := want.At(p.X, p.Y).RGBA()
		er, eg, eb, ea := uint32(c)*0x101, uint32(c)*0x101, uint32(255)*0x101, uint32(255)*0x101
		if wr != er || wg != eg || wb != eb || wa != ea {
			t.Fatalf("original At%v: got %d,%d,%d,%d want %d,%d,%d,%d", p, wr, wg, wb, wa, er, eg, eb, ea)
		}
	}

	// Interpreted: every original method body runs here, driven by image/png
	// through the generated transport stubs.
	p := callbackTourLoad(t, dir, source)
	var out, errs bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, p.File); err != nil {
		t.Fatalf("interpreted: %v; stdout=%q stderr=%q", err, out.String(), errs.String())
	}
	if errs.Len() != 0 {
		t.Fatalf("interpreted: unexpected stderr %q", errs.String())
	}
	if out.String() != wantOut {
		t.Fatal("interpreted image differs from native")
	}
	callbackImageSame(t, "interpreted", callbackImageDecode(t, out.String()), want)

	res, err := lower.Compile(p.File, lower.Options{Origin: path, Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	generated, binary := filepath.Join(dir, "generated.go"), filepath.Join(dir, "compiled")
	if err = os.WriteFile(generated, res.Source, 0600); err != nil {
		t.Fatal(err)
	}
	build(generated, binary)
	if after, err := os.ReadFile(path); err != nil || string(after) != source {
		t.Fatal("original bytes changed")
	}
	// Source-free: neither the original nor the generated source, and no tools
	// on PATH, are available to the compiled artifact.
	for _, name := range []string{path, generated} {
		if err = os.Remove(name); err != nil {
			t.Fatal(err)
		}
	}
	gotOut := run(binary)
	if gotOut != wantOut {
		t.Fatal("compiled image differs from native")
	}
	callbackImageSame(t, "compiled", callbackImageDecode(t, gotOut), want)
}
