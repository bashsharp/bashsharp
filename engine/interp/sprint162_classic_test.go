// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build unix

package interp_test

// Sprint: #162; Story: #96; Story-ID: e838e8895341
//
// The isolation contract: a Classic-shaped script must behave
// byte-identically with the Bash++ dialect active. Every script under
// testdata/sprint162/classic/ is run under LangBash and under LangBashPP and
// stdout, stderr and the exit status must agree exactly. Each run is bounded
// by a deadline, so a hang is a FAIL rather than a stuck test.

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

type classicRun struct {
	stdout, stderr string
	status         int
	err            error
}

func runClassicScript(t *testing.T, lang syntax.LangVariant, name, source string) classicRun {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(lang)).Parse(strings.NewReader(source), name)
	if err != nil {
		t.Fatalf("%s: parse under %v: %v", name, lang, err)
	}
	var stdout, stderr bytes.Buffer
	r, err := interp.New(
		interp.Lang(lang),
		interp.Dir(t.TempDir()),
		interp.StdIO(nil, &stdout, &stderr),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err = r.Run(ctx, f)
	status := 0
	var exit interp.ExitStatus
	if errors.As(err, &exit) {
		status, err = int(exit), nil
	}
	return classicRun{stdout: stdout.String(), stderr: stderr.String(), status: status, err: err}
}

func TestSprint162ClassicIsolation(t *testing.T) {
	root := filepath.Join("testdata", "sprint162", "classic")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	scripts := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sh") {
			continue
		}
		scripts++
		source, err := os.ReadFile(filepath.Join(root, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		t.Run(strings.TrimSuffix(entry.Name(), ".sh"), func(t *testing.T) {
			t.Parallel()
			classic := runClassicScript(t, syntax.LangBash, entry.Name(), string(source))
			if classic.err != nil {
				t.Fatalf("Classic run failed: %v\nstdout=%q\nstderr=%q", classic.err, classic.stdout, classic.stderr)
			}
			if classic.stdout == "" {
				t.Fatalf("Classic run produced no output; the reproducer is not observable")
			}
			on := runClassicScript(t, syntax.LangBashPP, entry.Name(), string(source))
			if on.err != nil {
				t.Fatalf("Bash++ run failed: %v\nstdout=%q\nstderr=%q", on.err, on.stdout, on.stderr)
			}
			if on.stdout != classic.stdout || on.stderr != classic.stderr || on.status != classic.status {
				t.Fatalf("Bash++ activation changed Classic behaviour\nClassic: status=%d stdout=%q stderr=%q\nBash++:  status=%d stdout=%q stderr=%q",
					classic.status, classic.stdout, classic.stderr, on.status, on.stdout, on.stderr)
			}
		})
	}
	if scripts == 0 {
		t.Fatal("no reproducers found")
	}
}

// The rule boundary: once the File has had a task, a FIFO open by the shell
// is the registered-peer rendezvous again. A process substitution's end is
// published to the group, so the shell's own redirection over it still
// pairs, and an external consumer of the substitution still opens the FIFO
// natively; an external peer of a named FIFO stays unsupported, as before.
func TestSprint162BashPPFIFOAfterTask(t *testing.T) {
	const prelude = `
func noop(ack) { ack <- ok; }
`
	for _, tc := range []struct {
		name, body, want, wantErr string
	}{
		{name: "procsub-redir", body: `read -r x < <(echo hello); echo "x=$x"`, want: "x=hello\n"},
		{name: "procsub-redir-out", body: `echo out > >(cat); wait; echo done`, want: "out\ndone\n"},
		{name: "procsub-in-task", body: `go consume(ack); <-ack`, want: "x=hello\n"},
		{name: "procsub-external-control", body: `cat <(echo viacat)`, want: "viacat\n"},
		{name: "fifo-subshell-peer-control", body: `mkfifo p; ( echo bg-writer > p ) & read -r line < p; echo "read: $line"; wait`, want: "read: bg-writer\n"},
		{name: "fifo-external-peer-unsupported", body: `mkfifo p; cat p & echo x > p; wait; echo done`,
			wantErr: "external or unregistered peers are unsupported"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			src := prelude + `
func consume(ack) { cat < <(echo x=hello); ack <- ok; }
func main() {
 ack := make(chan string)
 go noop(ack)
 <-ack
 ` + tc.body + `
}
main()
`
			var out bytes.Buffer
			r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), tc.name+".bpp")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = r.Run(ctx, f)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(out.String(), tc.wantErr) || strings.Contains(out.String(), "done") {
					t.Fatalf("out=%q err=%v", out.String(), err)
				}
				return
			}
			if err != nil || out.String() != tc.want {
				t.Fatalf("out=%q err=%v, want %q", out.String(), err, tc.want)
			}
		})
	}
}
