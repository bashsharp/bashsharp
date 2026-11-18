// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Sprint 245, story 682 ($0 spelling): nameref.tests runs
// `for testfile in ./nameref[0-9].sub …; do ${THIS_SH} "$testfile"; done`
// and the child printed `.\nameref2.sub: line 5: …` on Windows. The spelling
// was flipped by the glob, which joined its matches with the OS separator;
// the exec handler passes argv through verbatim, bashy's runPath keeps the
// operand for $0 and converts a copy for the open only, and the runner's
// diagnostic prefix is the filename it was given. These tests pin every
// hop on every host — the Windows CI leg proves the glob against a real
// directory.

func TestStory682GlobKeepsCallerSpelling(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, name := range []string{"nameref2.sub", "nameref3.sub", "nameref12.sub", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("echo $0\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file := parse(t, nil, `
for testfile in ./nameref[0-9].sub ./nameref[1-9][0-9].sub ; do
	echo "$testfile"
done
for testfile in nameref[0-9].sub; do echo "$testfile"; done
echo ./literal/../name
`)
	var out bytes.Buffer
	r, err := interp.New(interp.Dir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run error: %v\noutput: %q", err, out.String())
	}
	want := "./nameref2.sub\n./nameref3.sub\n./nameref12.sub\nnameref2.sub\nnameref3.sub\n./literal/../name\n"
	if got := out.String(); got != want {
		t.Fatalf("glob spelling:\n got: %q\nwant: %q", got, want)
	}
}

func TestStory682DiagnosticPrefixKeepsFilenameSpelling(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"./nameref2.sub", "sub/./nameref2.sub", "/c/work/nameref2.sub", `.\odd.sub`} {
		// bashy parses the script with its operand as the file name and runs
		// the *syntax.File; the runner's prefix is that name, untouched.
		file, err := syntax.NewParser().Parse(strings.NewReader("typeset -n ref=foo\nreadonly ref\nfoo=4\n"), name)
		if err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		r, err := interp.New(
			interp.StdIO(nil, &out, &out),
			interp.WithBashCompatErrors(true),
		)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
		_ = r.Run(ctx, file)
		cancel()
		if got, want := out.String(), name+": line 3: foo: readonly variable\n"; !strings.HasPrefix(got, want) {
			t.Errorf("diagnostic for filename %q:\n got: %q\nwant prefix: %q", name, got, want)
		}
	}
}
