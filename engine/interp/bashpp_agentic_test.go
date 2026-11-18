package interp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func agenticRunner(t *testing.T) (*Runner, *strings.Builder) {
	t.Helper()
	var out strings.Builder // bashpp-racegate:safe-synchronized
	r, err := New(Lang(syntax.LangBashPP), StdIO(nil, &out, &out), ExecHandler(func(ctx context.Context, args []string) error {
		hc := HandlerCtx(ctx)
		if len(args) == 2 && args[1] == "right" && hc.Stdin != nil {
			if _, err := io.Copy(io.Discard, hc.Stdin); err != nil {
				return err
			}
		}
		_, err := fmt.Fprintf(hc.Stdout, "%s:%t\n", strings.Join(args, "/"), hc.Agentic)
		return err
	}))
	if err != nil {
		t.Fatal(err)
	}
	return r, &out
}

func runAgentic(t *testing.T, r *Runner, src string) error {
	t.Helper()
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "agentic.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return r.Run(context.Background(), f)
}

func TestBashPPAgenticScopes(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"block current shell", "scope before\nagentic { x=kept; scope inside; agentic { scope nested; }; }\nscope $x", "scope/before:false\nscope/inside:true\nscope/nested:true\nscope/kept:false\n"},
		{"functions", "agentic func f() { scope typed }\nagentic function shell() { scope shell; }\nfunc helper() { scope helper; agentic { scope own; } }\nagentic { f(); shell; helper(); scope caller; }\nscope end", "scope/typed:true\nscope/shell:true\nscope/helper:false\nscope/own:true\nscope/caller:true\nscope/end:false\n"},
		{"ordinary shell helper", "helper() { scope helper; }\nagentic { helper; scope caller; }", "scope/helper:false\nscope/caller:true\n"},
		{"definition does not capture", "agentic { function plain() { scope plain; }; }\nplain", "scope/plain:false\n"},
		{"defer scheduling scope", "agentic func later() { scope deferred }\nfunc plain() { agentic { defer later(); }; scope body }\nplain()", "scope/body:false\nscope/deferred:true\n"},
		{"closure explicit body", "func main() { f := func() { scope closure; agentic { scope own; } }; agentic { f(); } }\nmain()", "scope/closure:false\nscope/own:true\n"},
		{"value retains declaration", "agentic func marked() { scope value }\nf := marked\nagentic { f(); }", "scope/value:true\n"},
		{"method and interface", "type T int\nagentic func (v T) Show() { scope method }\ntype I interface { Show() }\nvar v T = 1\nvar i I = v\nf := v.Show\nagentic { v.Show(); f(); i.Show(); }", "scope/method:true\nscope/method:true\nscope/method:true\n"},
		{"eval", "agentic { eval 'scope eval'; eval 'agentic { scope inner; }'; }\neval 'scope end'", "scope/eval:true\nscope/inner:true\nscope/end:false\n"},
		{"subshell", "agentic { (scope child); scope parent; }\nscope end", "scope/child:true\nscope/parent:true\nscope/end:false\n"},
		{"pipeline", "agentic { scope left | scope right; }\nscope end", "scope/right:true\nscope/end:false\n"},
		{"command substitution", "agentic { echo \"$(scope child)\"; }\nscope end", "scope/child:true\nscope/end:false\n"},
		{"errexit group status", "set -e\nagentic { false && true; }\nscope end", "scope/end:false\n"},
		{"return restores", "agentic function early() { scope early; return 3; }\nagentic { early; scope caller; }\nscope end", "scope/early:true\nscope/caller:true\nscope/end:false\n"},
		{"failure restores", "agentic { false; }\nscope end", "scope/end:false\n"},
		{"redefinition clears", "agentic function f() { scope bad; }\nf() { scope plain; }\nf", "scope/plain:false\n"},
		{"unset and recreate", "agentic function f() { scope bad; }\nunset -f f\nf() { scope plain; }\nf", "scope/plain:false\n"},
		{"ordinary function alias", "func f() { scope plain }\nx := f\nx()", "scope/plain:false\n"},
		{"alias chain", "agentic func f() { scope marked }\nx := f\ny := x\nagentic { y(); }", "scope/marked:true\n"},
		{"higher order callback", "agentic func f(n int) { scope $n }\nfunc apply(cb func) { agentic { cb(7); } }\nx := f\napply($x)", "scope/7:true\n"},
		{"returned handle", "agentic func f(n int) { scope $n }\nfunc factory() func { x := f; return $x }\nx := factory()\nagentic { x(8); }", "scope/8:true\n"},
		{"shadowed function value", "func f() { scope bad }\nfunc main() { var f string = shadow; x := f; echo $x; }\nmain()", "shadow\n"},
		{"classic declaration after alias", "func f() { scope bad }\nx := f\nx() { scope shell; }\nx", "scope/shell:false\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out := agenticRunner(t)
			if err := runAgentic(t, r, tc.src); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if r.bashPPAgentic {
				t.Fatal("scope leaked")
			}
		})
	}
}

func TestBashPPAgenticDenied(t *testing.T) {
	for _, src := range []string{
		"agentic func f() { scope forbidden }\nf()",
		"agentic function f() { scope forbidden; }\nf",
		"agentic func f() { scope forbidden }\nx := f\nx()",
		"agentic func f() { scope forbidden }\nfunc helper() { f(); }\nagentic { helper(); }",
		"type T int\nagentic func (v T) M() { scope forbidden }\nvar v T = 1\nf := v.M\nf()",
		"type T int\nagentic func (v T) M() { scope forbidden }\ntype I interface { M() }\nvar v T = 1\nvar i I = v\ni.M()",
		"agentic func f(n int) { scope forbidden }\nfunc apply(cb func) { cb(7); }\nx := f\nagentic { apply($x); }",
		"agentic func f(n int) { scope forbidden }\nfunc factory() func { x := f; return $x }\nx := factory()\nx(8)",
	} {
		t.Run(src, func(t *testing.T) {
			r, out := agenticRunner(t)
			err := runAgentic(t, r, src)
			if err == nil {
				t.Fatal("call succeeded")
			}
			if !strings.Contains(out.String(), "requires an explicit agentic") || strings.Contains(out.String(), "forbidden:") {
				t.Fatalf("unexpected diagnostic %q", out)
			}
			if r.bashPPAgentic {
				t.Fatal("scope leaked")
			}
		})
	}
}

func TestBashPPAgenticSourceAndSerialization(t *testing.T) {
	r, out := agenticRunner(t)
	path := filepath.Join(t.TempDir(), "sourced.bpp")
	if err := os.WriteFile(path, []byte("scope sourced\nagentic { scope opted; }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := runAgentic(t, r, "agentic { source "+shellQuote(path)+"; scope caller; }\nscope end"); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "scope/sourced:false\nscope/opted:true\nscope/caller:true\nscope/end:false\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	out.Reset()
	if err := runAgentic(t, r, "agentic function f() { scope restored; }\ndeclare -f f"); err != nil {
		t.Fatal(err)
	}
	definition := out.String()
	if !strings.HasPrefix(definition, "agentic function f") {
		t.Fatalf("marker lost: %s", definition)
	}
	out.Reset()
	if err := runAgentic(t, r, definition+"\nagentic { f; }"); err != nil {
		t.Fatal(err)
	}
	if out.String() != "scope/restored:true\n" {
		t.Fatal(out.String())
	}
	out.Reset()
	if err := runAgentic(t, r, "export -f f"); err == nil || !strings.Contains(out.String(), "cannot export") {
		t.Fatalf("export lost contract: %v, %s", err, out)
	}
}

func TestBashPPAgenticTaskScope(t *testing.T) {
	r, out := agenticRunner(t)
	err := runAgentic(t, r, "agentic func worker(ch) { scope worker; ch <- done }\nfunc main() { ch := make(chan string); agentic { go worker(ch); }; v := <-ch; scope $v }\nmain()\nscope end")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "scope/worker:true\nscope/done:false\nscope/end:false\n"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBashPPAgenticUnwindAndReuse(t *testing.T) {
	for _, src := range []string{
		"agentic { exit 7; }",
		"agentic func fail() { panic(boom); }\nagentic { fail(); }",
	} {
		t.Run(src, func(t *testing.T) {
			r, out := agenticRunner(t)
			if err := runAgentic(t, r, src); err == nil {
				t.Fatal("expected failure")
			}
			if r.bashPPAgentic {
				t.Fatal("scope leaked after unwind")
			}
			out.Reset()
			if err := runAgentic(t, r, "scope reused"); err != nil {
				t.Fatal(err)
			}
			if out.String() != "scope/reused:false\n" {
				t.Fatal(out.String())
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r, err := New(Lang(syntax.LangBashPP), ExecHandler(func(ctx context.Context, args []string) error {
		if !HandlerCtx(ctx).Agentic {
			t.Error("scope not visible at cancellation")
		}
		cancel()
		return ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader("agentic { cancel-now; }"), "cancel.bpp")
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(ctx, f); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if r.bashPPAgentic {
		t.Fatal("scope leaked after cancellation")
	}
	if err := runAgentic(t, r, ":"); err != nil {
		t.Fatal(err)
	}
}

func TestBashPPAgenticTrapCallbacks(t *testing.T) {
	for _, name := range []string{"ERR", "DEBUG", "RETURN", "EXIT"} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", name, explicit), func(t *testing.T) {
				r, out := agenticRunner(t)
				callback := "scope callback"
				if explicit {
					callback = "agentic { scope callback; }"
				}
				src := "set -T\nplain() { :; }\ntrap '" + callback + "' " + name + "\nagentic { plain; scope inside; false; }\n:"
				if err := runAgentic(t, r, src); err != nil {
					t.Fatal(err)
				}
				want := fmt.Sprintf("scope/callback:%t", explicit)
				found := false
				for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
					if strings.HasPrefix(line, "scope/callback:") {
						found = true
						if line != want {
							t.Fatalf("callback inherited scope: %s", out)
						}
					}
				}
				if !found || !strings.Contains(out.String(), "scope/inside:true") {
					t.Fatalf("callback/caller did not execute: %s", out)
				}
				if r.bashPPAgentic {
					t.Fatal("callback leaked scope")
				}
			})
		}
	}
	r, out := agenticRunner(t)
	if err := runAgentic(t, r, "agentic function marked() { scope forbidden; }\ntrap marked ERR\nagentic { false; }"); err == nil {
		t.Fatal("expected original false status")
	}
	if strings.Contains(out.String(), "scope/forbidden") || !strings.Contains(out.String(), "requires an explicit agentic") {
		t.Fatal(out.String())
	}
}

// Call the existing signal-delivery entry directly: this tests its scope and
// control-flow restoration without installing or sending process-wide signals.
func TestBashPPAgenticSignalCallback(t *testing.T) {
	for _, callback := range []string{"scope callback", "agentic { scope callback; }", "agentic { exit 7; }", "agentic {"} {
		t.Run(callback, func(t *testing.T) {
			r, out := agenticRunner(t)
			if err := runAgentic(t, r, ":"); err != nil {
				t.Fatal(err)
			}
			r.bashPPAgentic = true
			r.runSignalTrap(context.Background(), callback, "USR1")
			if !r.bashPPAgentic {
				t.Fatal("interrupted scope not restored")
			}
			switch callback {
			case "scope callback":
				if out.String() != "scope/callback:false\n" {
					t.Fatal(out.String())
				}
			case "agentic { scope callback; }":
				if out.String() != "scope/callback:true\n" {
					t.Fatal(out.String())
				}
			case "agentic { exit 7; }":
				if !r.exit.exiting || r.exit.code != 7 {
					t.Fatalf("signal control flow changed: %+v", r.exit)
				}
			}
		})
	}
}

func TestBashPPAgenticMapfileCallback(t *testing.T) {
	for _, command := range []string{"mapfile", "readarray"} {
		for _, tc := range []struct{ callback, want string }{
			{"scope", "scope/0/line:false\n"},
			{"agentic { scope own; }; :", "scope/own:true\n"},
		} {
			t.Run(command+"/"+tc.callback, func(t *testing.T) {
				r, out := agenticRunner(t)
				src := "agentic { " + command + " -t -C '" + tc.callback + "' -c 1 <<< line; scope caller; }\nscope end"
				if err := runAgentic(t, r, src); err != nil {
					t.Fatal(err)
				}
				if got, want := out.String(), tc.want+"scope/caller:true\nscope/end:false\n"; got != want {
					t.Fatalf("got %q want %q", got, want)
				}
			})
		}
		r, out := agenticRunner(t)
		_ = runAgentic(t, r, "agentic function marked() { scope forbidden; }\nagentic { "+command+" -C marked -c 1 <<< line; }")
		if strings.Contains(out.String(), "scope/forbidden") || !strings.Contains(out.String(), "requires an explicit agentic") {
			t.Fatal(out.String())
		}
	}
}
