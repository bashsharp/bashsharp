package interp

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestDeclarationEscapeByte(t *testing.T) {
	// Keep fixed expectations authoritative even where the GNU oracle is absent.
	gnu := ""
	for _, candidate := range []string{"/opt/homebrew/bin/bash", "bash"} {
		out, err := exec.Command(candidate, "--version").Output()
		if err == nil && strings.Contains(string(out), "GNU bash, version 5.3") && !strings.Contains(string(out), "bashy") {
			gnu = candidate
			break
		}
	}
	for _, tc := range []struct{ name, value, quoted string }{
		{"escape", "\x1b", `$'\E'`},
		{"adjacent_digits", "\x1b033", `$'\E033'`},
		{"literal_and_escape", `\033` + "\x1b", `$'\\033\E'`},
		{"escape_and_literal", "\x1b" + `\033`, `$'\E\\033'`},
		{"neighbor_control", "\x1b\x1c", `$'\E\034'`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, command := range []string{"export -p", "declare -p ESCAPE", "readonly -p"} {
				var out bytes.Buffer
				r, err := New(Env(expand.ListEnviron("ESCAPE="+tc.value)), StdIO(nil, &out, &out))
				if err != nil {
					t.Fatal(err)
				}
				src := "readonly ESCAPE; " + command
				f, err := syntax.NewParser().Parse(strings.NewReader(src), "")
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Run(context.Background(), f); err != nil {
					t.Fatal(err)
				}
				if !strings.Contains(out.String(), " ESCAPE="+tc.quoted+"\n") {
					t.Fatalf("%s: %q; want value %s", command, out.String(), tc.quoted)
				}
				if gnu != "" {
					oracle := exec.Command(gnu, "--noprofile", "--norc", "-c", src)
					oracle.Env = []string{"ESCAPE=" + tc.value, "LC_ALL=C"}
					got, err := oracle.Output()
					if err != nil {
						t.Fatal(err)
					}
					if !strings.Contains(string(got), " ESCAPE="+tc.quoted+"\n") {
						t.Fatalf("GNU %s: %q; want value %s", command, got, tc.quoted)
					}
				}
				out.Reset()
				f, err = syntax.NewParser().Parse(strings.NewReader("value="+tc.quoted+"; printf '%s' \"$value\""), "")
				if err != nil {
					t.Fatal(err)
				}
				if err := r.Run(context.Background(), f); err != nil {
					t.Fatal(err)
				}
				if out.String() != tc.value {
					t.Fatalf("roundtrip: %q, want %q", out.String(), tc.value)
				}
			}
		})
	}
}
