package expand

import (
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
)

// Glob matches are shell words: joined with `/` on every platform, never
// with the OS separator (bash prints `foo/bar` for `f*/bar` on Windows too).
func TestPathJoin2KeepsSlash(t *testing.T) {
	cases := []struct {
		a, b, want string
		windows    bool
	}{
		{"", "x", "x", false},
		{"foo", "bar", "foo/bar", false},
		{"foo/", "bar", "foo/bar", false},
		{"/", "etc", "/etc", false},
		{"lib/glob", "glob.o", "lib/glob/glob.o", true},
		{`C:\`, "bar", `C:\bar`, true},
		{"C:/", "bar", "C:/bar", true},
		{`dir\`, "x", `dir\/x`, false}, // a backslash is not a separator off Windows
	}
	for _, c := range cases {
		if got := pathJoin2Mode(c.a, c.b, c.windows); got != c.want {
			t.Errorf("pathJoin2Mode(%q, %q, windows=%v) = %q, want %q", c.a, c.b, c.windows, got, c.want)
		}
	}
}

// A glob pattern splits on `/` only: a backslash is the pattern's escape
// (`"*"*` reaches the globber as `\**`), never a separator, on Windows too.
func TestPathSplitKeepsBackslashEscapes(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{`\**`, []string{`\**`}},
		{"foo/bar", []string{"foo", "bar"}},
		{"/c/Users/*", []string{"", "c", "Users", "*"}},
	}
	for _, c := range cases {
		got := pathSplit(c.in)
		if len(got) != len(c.want) {
			t.Errorf("pathSplit(%q) = %q, want %q", c.in, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("pathSplit(%q) = %q, want %q", c.in, got, c.want)
				break
			}
		}
	}
	if !globPathAbs("/tmp/x") || !globPathAbs("/c/x") || globPathAbs("rel/x") {
		t.Error("globPathAbs: a leading slash is absolute in the shell's spelling; a relative element is not")
	}
}

// The directory a glob walk reads next is in the shell's spelling: on
// Windows $PWD keeps the POSIX form (/tmp/x) and the directory reader
// converts it, so the join must not respell it with backslashes — the
// converter reads `\tmp\x` as the drive-relative native C:\tmp\x, not the
// /tmp mount, and `ls lib/**` after `cd $TMPDIR/x` found nothing
// (globstar.tests).
func TestGlobPathJoinKeepsShellSpelling(t *testing.T) {
	cases := []struct {
		base, p, want string
		windows       bool
	}{
		{"/tmp/globstar-1", "lib", "/tmp/globstar-1/lib", true},
		{"/tmp/globstar-1", "", "/tmp/globstar-1", true},
		{"/tmp/globstar-1", "lib/", "/tmp/globstar-1/lib", true},
		{"/c/Users/x", "lib/glob", "/c/Users/x/lib/glob", true},
		{"C:/Temp/x", "lib", "C:/Temp/x/lib", true},
		{"", "lib", "lib", true},
		{"/tmp/globstar-1", "/etc", "/etc", true}, // absolute elements stand alone
		{"/tmp/globstar-1", "lib", "/tmp/globstar-1/lib", false},
	}
	for _, c := range cases {
		if got := globPathJoinMode(c.base, c.p, c.windows); got != c.want {
			t.Errorf("globPathJoinMode(%q, %q, windows=%v) = %q, want %q", c.base, c.p, c.windows, got, c.want)
		}
	}
}

// globstar.tests, first command: `ls lib/**` after `cd $TMPDIR/globstar-$$`.
// The reader stands in for the interpreter's, which resolves the shell's
// spelling; a backslash-spelled probe is what the bug produced on Windows
// and what pathconv would send to C:\tmp\…, so it is refused here.
func TestGlobStarFromShellSpelledBase(t *testing.T) {
	tree := map[string][]fs.DirEntry{
		"/tmp/globstar-1": {
			&mockFileInfo{name: "alias.o"},
			&mockFileInfo{name: "builtins", typ: fs.ModeDir},
			&mockFileInfo{name: "lib", typ: fs.ModeDir},
		},
		"/tmp/globstar-1/builtins": {&mockFileInfo{name: "jobs.o"}},
		"/tmp/globstar-1/lib": {
			&mockFileInfo{name: "glob", typ: fs.ModeDir},
			&mockFileInfo{name: "sh", typ: fs.ModeDir},
		},
		"/tmp/globstar-1/lib/glob": {
			&mockFileInfo{name: "glob.o"},
			&mockFileInfo{name: "smatch.o"},
		},
		"/tmp/globstar-1/lib/sh": {&mockFileInfo{name: "clock.o"}},
	}
	probe := func(p string) (string, error) {
		if strings.Contains(p, `\`) {
			return "", fmt.Errorf("backslash-spelled probe %q: pathconv would read it as a drive-relative native path", p)
		}
		return strings.TrimSuffix(p, "/"), nil
	}
	cfg := &Config{
		GlobStar: true,
		ReadDir2: func(p string) ([]fs.DirEntry, error) {
			key, err := probe(p)
			if err != nil {
				t.Error(err)
				return nil, err
			}
			entries, ok := tree[key]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return entries, nil
		},
		IsSearchable: func(p string) bool {
			key, err := probe(p)
			if err != nil {
				t.Error(err)
				return false
			}
			_, ok := tree[key]
			return ok
		},
	}
	for _, tc := range []struct {
		pat  string
		want []string
	}{
		{"lib/**", []string{"lib/", "lib/glob", "lib/glob/glob.o", "lib/glob/smatch.o", "lib/sh", "lib/sh/clock.o"}},
		{"lib/**/*.o", []string{"lib/glob/glob.o", "lib/glob/smatch.o", "lib/sh/clock.o"}},
		{"**/*.o", []string{"alias.o", "builtins/jobs.o", "lib/glob/glob.o", "lib/glob/smatch.o", "lib/sh/clock.o"}},
	} {
		got, err := cfg.glob("/tmp/globstar-1", tc.pat)
		if err != nil {
			t.Fatalf("glob(%q): %v", tc.pat, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("glob(%q) = %q, want %q", tc.pat, got, tc.want)
		}
	}
}
