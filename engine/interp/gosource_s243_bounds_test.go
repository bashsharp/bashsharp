//go:build full

package interp_test

// Sprint: #243; Story: #674; Story-ID: 63073886bfce

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestS243Issue30116ExactOutput(t *testing.T) {
	root := filepath.Join(runtime.GOROOT(), "test", "fixedbugs")
	source, err := os.ReadFile(filepath.Join(root, "issue30116.go"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join(root, "issue30116.out"))
	if err != nil {
		t.Fatal(err)
	}
	got, stderr, err := runGoSource(t, "issue30116", string(source))
	if err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr)
	}
	if stderr != "" {
		t.Fatalf("stderr:\n%s", stderr)
	}
	if got != string(want) {
		firstDifferentLine(t, string(want), got)
	}
}

func TestS243MultipleInvalidSliceBounds(t *testing.T) {
	source := `package main
import "fmt"
func check(name string, f func()) {
	defer func() { fmt.Println(name, recover()) }()
	f()
}
func main() {
	s := []int{1, 2, 3}
	check("high-negative-wins", func() { low, high := int64(-9), int64(-7); _ = s[low:high] })
	check("high-cap-wins", func() { low, high := int64(-9), int64(7); _ = s[low:high] })
	check("max-negative-wins", func() { low, high, max := int64(-9), int64(-7), int64(-5); _ = s[low:high:max] })
	check("max-cap-wins", func() { low, high, max := int64(-9), int64(-7), int64(8); _ = s[low:high:max] })
	check("high-max-wins", func() { low, high, max := int64(-9), int64(2), int64(1); _ = s[low:high:max] })
}`
	want := "high-negative-wins runtime error: slice bounds out of range [:-7]\n" +
		"high-cap-wins runtime error: slice bounds out of range [:7] with capacity 3\n" +
		"max-negative-wins runtime error: slice bounds out of range [::-5]\n" +
		"max-cap-wins runtime error: slice bounds out of range [::8] with capacity 3\n" +
		"high-max-wins runtime error: slice bounds out of range [:2:1]\n"
	got, stderr, err := runGoSource(t, "multiple-invalid-bounds", source)
	if err != nil {
		t.Fatalf("run: %v\nstderr:\n%s", err, stderr)
	}
	if got != want {
		firstDifferentLine(t, want, got)
	}
}

func firstDifferentLine(t *testing.T, want, got string) {
	t.Helper()
	wantLines, gotLines := splitLines(want), splitLines(got)
	for i := 0; i < len(wantLines) || i < len(gotLines); i++ {
		var wantLine, gotLine string
		if i < len(wantLines) {
			wantLine = wantLines[i]
		}
		if i < len(gotLines) {
			gotLine = gotLines[i]
		}
		if wantLine != gotLine {
			t.Fatalf("first difference at line %d:\nwant: %q\n got: %q", i+1, wantLine, gotLine)
		}
	}
}

func splitLines(text string) []string {
	var lines []string
	start := 0
	for i := range text {
		if text[i] == '\n' {
			lines = append(lines, text[start:i])
			start = i + 1
		}
	}
	if start < len(text) {
		lines = append(lines, text[start:])
	}
	return lines
}
