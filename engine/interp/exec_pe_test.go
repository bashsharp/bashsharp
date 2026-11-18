// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The extensionless-PE exec decision, proven on any host: which files
// qualify, when a copy of the running binary is recognised, and how an
// alias is made and removed. The Windows exec itself is in
// exec_pe_windows_test.go.

func writePE(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := os.WriteFile(path, append([]byte("MZ"), body...), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestIsPEImage(t *testing.T) {
	t.Parallel()
	for probe, want := range map[string]bool{"MZ": true, "MZ\x90\x00": true, "M": false, "": false, "#!/bin/sh": false, "mz": false} {
		if got := isPEImage([]byte(probe)); got != want {
			t.Errorf("isPEImage(%q) = %v, want %v", probe, got, want)
		}
	}
}

func TestSameExecutableImage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	big := bytes.Repeat([]byte("image"), 2000) // beyond the 4 KiB head
	self := filepath.Join(dir, "self.exe")
	writePE(t, self, big)
	copied := filepath.Join(dir, "sh")
	writePE(t, copied, big)
	shorter := filepath.Join(dir, "shorter")
	writePE(t, shorter, big[:len(big)-1])
	head := append([]byte(nil), big...)
	head[10] ^= 1
	differentHead := filepath.Join(dir, "head")
	writePE(t, differentHead, head)
	tail := append([]byte(nil), big...)
	tail[len(tail)-1] ^= 1
	differentTail := filepath.Join(dir, "tail")
	writePE(t, differentTail, tail)

	for _, tt := range []struct {
		a, b string
		want bool
	}{
		{self, self, true},
		{self, copied, true},
		{copied, self, true},
		{self, shorter, false},
		{self, differentHead, false},
		// The comparison is size plus the head; a difference past the
		// head is not seen, by design (a copied shell is identical).
		{self, differentTail, true},
		{self, filepath.Join(dir, "missing"), false},
		{self, "", false},
		{"", self, false},
		{self, dir, false},
	} {
		if got := sameExecutableImage(tt.a, tt.b); got != tt.want {
			t.Errorf("sameExecutableImage(%q, %q) = %v, want %v", filepath.Base(tt.a), filepath.Base(tt.b), got, tt.want)
		}
	}
}

func TestPlanExtensionlessPEExec(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	tmp := t.TempDir()
	image := bytes.Repeat([]byte("self"), 3000)
	self := filepath.Join(dir, "bash.exe")
	writePE(t, self, image)
	probe := func(path string) []byte {
		data, err := readShebangProbe(path)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}

	// Not an extensionless PE image: a suffixed .exe, a script, a
	// non-image file with no suffix.
	script := filepath.Join(dir, "script")
	if err := os.WriteFile(script, []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{self, script} {
		if p, cleanup, ok := planExtensionlessPEExec(path, probe(path), self, tmp); ok || p != "" || cleanup != nil {
			t.Errorf("%s: planned %q, cleanup=%v, %v; want not applicable", filepath.Base(path), p, cleanup != nil, ok)
		}
	}

	// A copy of the running binary runs as the binary, with no alias.
	copied := filepath.Join(dir, "sh")
	writePE(t, copied, image)
	if p, cleanup, ok := planExtensionlessPEExec(copied, probe(copied), self, tmp); !ok || p != self || cleanup != nil {
		t.Fatalf("copied shell: planned %q, cleanup=%v, %v; want self", p, cleanup != nil, ok)
	}

	// Any other image runs through a hard link under tempDir, removed by
	// the cleanup.
	other := filepath.Join(dir, "other")
	writePE(t, other, bytes.Repeat([]byte("other"), 100))
	p, cleanup, ok := planExtensionlessPEExec(other, probe(other), self, tmp)
	if !ok || cleanup == nil {
		t.Fatalf("other image: planned %q, cleanup=%v, %v", p, cleanup != nil, ok)
	}
	if filepath.Dir(p) != tmp || !strings.HasPrefix(filepath.Base(p), "bashy-exec-") || !strings.HasSuffix(p, ".exe") {
		t.Fatalf("alias %q, want <tempDir>/bashy-exec-*.exe", p)
	}
	ia, _ := os.Stat(p)
	ib, _ := os.Stat(other)
	if ia == nil || !os.SameFile(ia, ib) {
		t.Fatalf("alias %q is not a hard link to %q", p, other)
	}
	cleanup()
	if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alias not removed: %v", err)
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatalf("removing the alias touched the target: %v", err)
	}

	// No self known (os.Executable failed): a copy of the shell is still
	// run, through an alias.
	if p, cleanup, ok := planExtensionlessPEExec(copied, probe(copied), "", tmp); !ok || p == self || cleanup == nil {
		t.Fatalf("no self: planned %q, cleanup=%v, %v", p, cleanup != nil, ok)
	} else {
		cleanup()
	}
}

func TestMakeExecAliasFallbacks(t *testing.T) {
	// Not parallel: pins linkExecAlias.
	dir := t.TempDir()
	target := filepath.Join(dir, "tool")
	writePE(t, target, []byte("tool image"))

	// tempDir unusable: the link goes beside the target.
	alias, remove, err := makeExecAlias(target, filepath.Join(dir, "missing-temp"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(alias) != dir {
		t.Fatalf("alias %q, want one beside the target", alias)
	}
	remove()
	if _, err := os.Stat(alias); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("alias not removed: %v", err)
	}

	// Links impossible (another volume): a copy under tempDir.
	old := linkExecAlias
	linkExecAlias = func(string, string) error { return errors.New("cross-device link") }
	defer func() { linkExecAlias = old }()
	tmp := t.TempDir()
	alias, remove, err = makeExecAlias(target, tmp)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(alias) != tmp || !strings.HasSuffix(alias, ".exe") {
		t.Fatalf("copy alias %q, want under tempDir with .exe", alias)
	}
	got, err := os.ReadFile(alias)
	if err != nil || string(got) != "MZtool image" {
		t.Fatalf("copy alias content %q, %v", got, err)
	}
	if ia, _ := os.Stat(alias); ia == nil || (runtime.GOOS != "windows" && ia.Mode()&0o100 == 0) {
		t.Fatalf("copy alias not executable: %v", ia)
	}
	remove()
	if _, err := os.Stat(alias); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy alias not removed: %v", err)
	}
	// Nowhere to put it at all.
	if _, _, err := makeExecAlias(target, filepath.Join(dir, "missing-temp")); err == nil {
		t.Fatal("makeExecAlias succeeded with no writable place for the alias")
	}
	// Reserved names are fresh and unique.
	a, err := reserveExecAlias(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := reserveExecAlias(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatalf("reserveExecAlias reused %q", a)
	}
	if _, err := os.Stat(a); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("reserved name %q left a file: %v", a, err)
	}
}
