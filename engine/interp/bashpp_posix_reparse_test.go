// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information.

package interp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPPOSIXReparse(t *testing.T) {
	const extension = "func f() { echo ACTIVE; }\nf()\n"
	const classic = "a=(one two); printf '%s\\n' \"${a[1]}\"\n"
	for _, mode := range []struct {
		name   string
		lang   syntax.LangVariant
		params []string
		setup  string
	}{
		{"latent-startup", syntax.LangBashPP, []string{"-o", "posix"}, ""},
		{"params-bashpp-before-posix", syntax.LangBash, []string{"-o", "bashpp", "-o", "posix"}, ""},
		{"params-posix-before-bashpp", syntax.LangBash, []string{"-o", "posix", "-o", "bashpp"}, ""},
		{"live-bashpp-before-posix", syntax.LangBash, nil, "set -o bashpp; set -o posix"},
		{"live-posix-before-bashpp", syntax.LangBash, nil, "set -o posix; set -o bashpp"},
	} {
		for _, route := range []string{"eval", "source", "."} {
			t.Run(mode.name+"/"+route, func(t *testing.T) {
				dir := t.TempDir()
				for name, body := range map[string]string{"extension.bpp": extension, "classic.sh": classic} {
					if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var stdout, stderr bytes.Buffer
				r, err := New(
					Lang(mode.lang), Params(mode.params...), Dir(dir),
					StdIO(nil, &stdout, &stderr), WithBashCompatErrors(true),
					// A rejected eval must leave this session able to read
					// the subsequent live option change.
					Interactive(true),
				)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				run := func(src string) error {
					t.Helper()
					// Only the builtin's new input may select Bash++;
					// the enclosing commands are ordinary Bash grammar.
					file, err := syntax.NewParser().Parse(strings.NewReader(src), "")
					if err != nil {
						t.Fatal(err)
					}
					return r.Run(ctx, file)
				}
				call := func(body, path string) string {
					if route == "eval" {
						return "eval '" + strings.ReplaceAll(body, "'", "'\\''") + "'"
					}
					return route + " ./" + path
				}
				if err := run(mode.setup); err != nil {
					t.Fatal(err)
				}
				if r.Dialect() != syntax.LangBashPP || r.LangVariant() != syntax.LangPOSIX {
					t.Fatalf("setup: latent=%v effective=%v", r.Dialect(), r.LangVariant())
				}
				// POSIX options retain the established Bash grammar, including
				// arrays, while suppressing Bash++ grammar in newly read input.
				if err := run(call(classic, "classic.sh")); err != nil {
					t.Fatalf("classic grammar: %v; stderr=%q", err, stderr.String())
				}
				if stdout.String() != "two\n" || stderr.Len() != 0 {
					t.Fatalf("classic grammar: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				stdout.Reset()
				stderr.Reset()
				err = run(call(extension, "extension.bpp"))
				var status ExitStatus
				if !errors.As(err, &status) || status != 1 {
					t.Fatalf("POSIX reparse: error=%v; stderr=%q", err, stderr.String())
				}
				if !strings.Contains(stderr.String(), "a command can only contain words and redirects") ||
					strings.Contains(stderr.String(), "extensions disabled") || stdout.Len() != 0 {
					t.Fatalf("want grammar rejection: stdout=%q stderr=%q", stdout.String(), stderr.String())
				}
				if r.bashPPFuncs["f"] != nil {
					t.Fatal("rejected input registered a typed function")
				}
				stdout.Reset()
				stderr.Reset()
				if err := run("set +o posix; " + call(extension, "extension.bpp")); err != nil {
					t.Fatalf("restored Bash++: %v; stderr=%q", err, stderr.String())
				}
				if r.LangVariant() != syntax.LangBashPP || stdout.String() != "ACTIVE\n" || stderr.Len() != 0 {
					t.Fatalf("restored Bash++: effective=%v stdout=%q stderr=%q", r.LangVariant(), stdout.String(), stderr.String())
				}
			})
		}
	}
}
