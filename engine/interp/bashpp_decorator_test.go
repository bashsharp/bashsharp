// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func runDecorated(t *testing.T, src string, opts ...interp.RunnerOption) (stdout, stderr string, err error) {
	t.Helper()
	f, parseErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "deco.bpp")
	qt.Assert(t, qt.IsNil(parseErr))
	var out, errs strings.Builder
	all := append([]interp.RunnerOption{interp.Lang(syntax.LangBashPP)}, opts...)
	r := bashPPRunner(t, &out, append(all, interp.StdIO(nil, &out, &errs))...)
	err = r.Run(context.Background(), f)
	return out.String(), errs.String(), err
}

func TestBashPPDecoratorTrace(t *testing.T) {
	const src = `func trace(c *Call) {
	name := c.Name
	echo "-> $name"
	c.Next()
	st := c.Status
	echo "<- $name status=$st"
}
@trace()
func deploy(env string) {
	echo "deploying $env"
}
deploy(prod)
`
	out, stderr, err := runDecorated(t, src)
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "-> deploy\ndeploying prod\n<- deploy status=0\n"))
}

func TestBashPPDecoratorScripts(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			"stack is outermost first, per-call args, late resolution",
			`@tag(x)
@tag(y)
func f() { echo body; }
func tag(c *Call, label string) {
	echo "in $label"
	c.Next()
	echo "out $label"
}
f()
`,
			"in x\nin y\nbody\nout y\nout x\n",
		},
		{
			"arguments evaluate per invocation in the captured scope",
			`n := 1
func tag(c *Call, label string) { echo "tag $label"; c.Next(); }
@tag($n)
func f() { :; }
f()
n=2
f()
`,
			"tag 1\ntag 2\n",
		},
		{
			"typed target args evaluate in the declaration scope, not the target parameters",
			`label := "outer"
func tag(c *Call, v string) { echo "tag $v"; c.Next(); }
@tag($label)
func f(label string) { echo "body $label"; }
f(inner)
`,
			"tag outer\nbody inner\n",
		},
		{
			"typed target args do not see decorator locals",
			`x := "glob"
func outerd(c *Call) { x := "shadow"; echo "outer $x"; c.Next(); }
func innerd(c *Call, v string) { echo "inner $v"; c.Next(); }
@outerd()
@innerd($x)
func f() { :; }
f()
`,
			"outer shadow\ninner glob\n",
		},
		{
			"shell target args stay dynamically scoped",
			`func tag(c *Call, v string) { echo "tag $v"; c.Next(); }
@tag($y)
function s() { :; }
caller() { local y="dyn"; s; }
y="top"
caller
s
`,
			"tag dyn\ntag top\n",
		},
		{
			"repeated Next freshly binds body args",
			`func twice(c *Call) { c.Next(); c.Next(); }
@twice()
func f(n int) { echo "n=$n"; n=9; }
f(3)
`,
			"n=3\nn=3\n",
		},
		{
			"repeated Next keeps the return and defer lifecycle",
			`func twice(c *Call) { c.Next(); c.Next(); echo "after"; }
func cleanup() { echo "cleanup"; }
@twice()
func f() { defer cleanup(); echo "body"; return; }
f()
echo "done"
`,
			// Each Next is an ordinary invocation boundary, so its target
			// defers finish before control returns to the decorator.
			"body\ncleanup\nbody\ncleanup\nafter\ndone\n",
		},
		{
			"repeated Next feeds shell positionals freshly",
			`func twice(c *Call) { c.Next(); c.Next(); }
@twice()
function f() { echo "1=$1"; set -- changed; }
f start
`,
			"1=start\n1=start\n",
		},
		{
			"args rewrite reaches shell positional parameters",
			`func grow(c *Call) { c.Args = []any{"x", "y"}; c.Next(); }
@grow()
function s() { echo "$# -> $*"; }
s one
`,
			"2 -> x y\n",
		},
		{
			"args rewrite rebinds a variadic target",
			`func grow(c *Call) { c.Args = []any{"h", "1", "9"}; c.Next(); }
@grow()
func v(head string, rest ...int) { echo "$head ${rest[*]} n=${#rest[@]}"; }
v(h, 1)
`,
			"h 1 9 n=2\n",
		},
		{
			"variadic target unchanged through a no-op decorator",
			`func noopd(c *Call) { c.Next(); }
@noopd()
func v(head string, rest ...int) { echo "$head ${rest[*]} n=${#rest[@]}"; }
v(a, 1, 2)
`,
			"a 1 2 n=2\n",
		},
		{
			"keyword and default arguments",
			`func retry(c *Call, n int = 1, backoff string = "0") {
	i := 0
	while [ "$i" -lt "$n" ]; do
		c.Next()
		st := c.Status
		if [ "$st" -eq 0 ]; then return; fi
		i=$((i+1))
	done
}
attempts=0
@retry(n: 3)
func flaky() {
	attempts=$((attempts+1))
	echo "attempt $attempts"
	[ "$attempts" -ge 2 ]
}
flaky()
echo "status=$? attempts=$attempts"
`,
			"attempt 1\nattempt 2\nstatus=0 attempts=2\n",
		},
		{
			"results travel through the context",
			`func trace(c *Call) {
	c.Next()
	r := c.Results[0]
	echo "saw $r"
}
@trace()
func pick(a, b int) int { return $b }
x := pick(1, 7)
echo "x=$x"
`,
			"saw 7\nx=7\n",
		},
		{
			"result rewrite",
			`func upper(c *Call) {
	c.Next()
	c.Results = []any{"REWRITTEN"}
}
@upper()
func greet() string { return hello }
x := greet()
echo "x=$x"
`,
			"x=REWRITTEN\n",
		},
		{
			"skip denies the body and sets status",
			`func deny(c *Call) {
	echo "denied"
	c.Status = 3
}
@deny()
func secret() int { echo leaked; return 1 }
x := secret()
echo "x=<$x> status=$?"
`,
			"denied\nx=<0> status=3\n",
		},
		{
			"args are the bound parameters",
			`func show(c *Call) {
	args := c.Args
	n := len(args)
	a := c.Args[0]
	b := c.Args[1]
	echo "args=$a $b n=$n"
	c.Next()
}
@show()
func f(a string, b int) { :; }
f(x, 2)
`,
			"args=x 2 n=2\n",
		},
		{
			"shell function sees $@ and status",
			`func trace(c *Call) {
	name := c.Name
	a := c.Args[0]
	echo "-> $name $a"
	c.Next()
	st := c.Status
	echo "<- $name status=$st"
}
@trace()
function backup() { echo "backing up $1"; return 4; }
backup disk
echo "status=$?"
`,
			"-> backup disk\nbacking up disk\n<- backup status=4\nstatus=4\n",
		},
		{
			"shell function posix spelling and decorator status rewrite",
			`func fix(c *Call) { c.Next(); c.Status = 0; }
@fix()
b() { false; }
b
echo "status=$?"
`,
			"status=0\n",
		},
		{
			"method and indirect routes",
			`type T int
func trace(c *Call) { name := c.Name; echo "trace $name"; c.Next(); }
@trace()
func (v T) Show() { echo "show" }
type I interface { Show() }
var v T = 1
var i I = v
f := v.Show
v.Show()
f()
i.Show()
`,
			"trace T.Show\nshow\ntrace T.Show\nshow\ntrace T.Show\nshow\n",
		},
		{
			"function value keeps the decoration",
			`func trace(c *Call) { echo "traced"; c.Next(); }
@trace()
func f() { echo body }
g := f
g()
`,
			"traced\nbody\n",
		},
		{
			"decorator frames are visible in FUNCNAME",
			`func trace(c *Call) { c.Next(); }
@trace()
function f() { echo "${FUNCNAME[0]} ${FUNCNAME[1]}"; }
f
`,
			"f trace\n",
		},
		{
			"agentic declaration runs inside its scope and reports Agentic",
			`func trace(c *Call) { a := c.Agentic; echo "agentic=$a"; c.Next(); }
@trace()
agentic func act() { echo acted }
agentic { act(); }
`,
			"agentic=true\nacted\n",
		},
		{
			"recursion re-enters the chain",
			`func trace(c *Call) { n := c.Args[0]; echo "enter $n"; c.Next(); }
@trace()
func down(n int) { if [ "$n" -gt 0 ]; then down($((n-1))); fi }
down(2)
`,
			"enter 2\nenter 1\nenter 0\n",
		},
		{
			"body defers run when the target returns",
			`func trace(c *Call) { c.Next(); echo "after next"; }
func cleanup() { echo cleanup }
@trace()
func f() { defer cleanup(); echo body }
f()
echo done
`,
			// The target call unwinds before Next returns to its decorator.
			"body\ncleanup\nafter next\ndone\n",
		},
		{
			"redefinition drops the decoration",
			`func trace(c *Call) { echo traced; c.Next(); }
@trace()
function f() { echo one; }
f
f() { echo two; }
f
`,
			"traced\none\ntwo\n",
		},
		{
			"unset -f drops the decoration",
			`func trace(c *Call) { echo traced; c.Next(); }
@trace()
function f() { echo one; }
unset -f f
f() { echo two; }
f
`,
			"two\n",
		},
		{
			"declare -f prints source decorators",
			`func trace(c *Call) { c.Next(); }
@trace(level: 2)
function f() { echo one; }
declare -f f
`,
			"@trace(level: 2)\nf () \n{ \n    echo one\n}\n",
		},
		{
			"subshell inherits the table",
			`func trace(c *Call) { echo traced; c.Next(); }
@trace()
function f() { echo body; }
( f )
`,
			"traced\nbody\n",
		},
		{
			"user Call type shadows the predeclared one",
			`type Call struct { Name string }
func mine(c *Call) { n := c.Name; echo "mine $n"; }
func main() {
	v := Call{Name: "shadowed"}
	p := &v
	mine(p)
}
main()
`,
			"mine shadowed\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runDecorated(t, tc.src)
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

func TestBashPPDecoratorDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{"undefined", "@missing()\nfunc f() { echo leaked }\nf()\n", "BASHPP-EDECO-UNDEF"},
		{"shell function is not a decorator", "d() { :; }\n@d()\nfunc f() { echo leaked }\nf()\n", "BASHPP-EDECO-SIG"},
		{"wrong first parameter", "func d(n int) { :; }\n@d()\nfunc f() { echo leaked }\nf()\n", "BASHPP-EDECO-SIG"},
		{"shadowed Call is not the predeclared one", "type Call struct { X int }\nfunc d(c *Call) { :; }\n@d()\nfunc f() { echo leaked }\nf()\n", "BASHPP-EDECO-SIG"},
		{"self", "@d()\nfunc d(c *Call) { c.Next(); }\n", "BASHPP-EDECO-SELF"},
		{"reserved namespace", "@ns.d()\nfunc f() { :; }\n", "BASHPP-EDECO-RESERVED"},
		{"cycle", "@b()\nfunc a(c *Call) { c.Next(); }\n@a()\nfunc b(c *Call) { c.Next(); }\n@a()\nfunc f() { echo leaked }\nf()\n", "BASHPP-EDECO-CYCLE"},
		{"args arity mutation", "func d(c *Call) { c.Args = []any{\"1\", \"2\"}; c.Next(); }\n@d()\nfunc f(n int) { echo leaked }\nf(1)\n", "BASHPP-EDECO-ARG"},
		{"args variadic arity mutation", "func d(c *Call) { c.Args = []any{}; c.Next(); }\n@d()\nfunc f(head string, rest ...int) { echo leaked }\nf(h)\n", "BASHPP-EDECO-ARG"},
		{"args type mutation", "func d(c *Call) { c.Args = []any{\"nope\"}; c.Next(); }\n@d()\nfunc f(n int) { echo leaked }\nf(1)\n", "BASHPP-EDECO-ARG"},
		{"args variadic element type mutation", "func d(c *Call) { c.Args = []any{\"h\", \"nope\"}; c.Next(); }\n@d()\nfunc f(head string, rest ...int) { echo leaked }\nf(h)\n", "BASHPP-EDECO-ARG"},
		{"result count", "func d(c *Call) { c.Next(); c.Results = []any{\"a\", \"b\"}; }\n@d()\nfunc f() int { return 1 }\nx := f()\n", "BASHPP-EDECO-RESULT"},
		{"result type", "func d(c *Call) { c.Next(); c.Results = []any{\"nope\"}; }\n@d()\nfunc f() int { return 1 }\nx := f()\n", "BASHPP-EDECO-RESULT"},
		{"results for a result-less function", "func d(c *Call) { c.Next(); c.Results = []any{\"x\"}; }\n@d()\nfunc f() { :; }\nf()\n", "BASHPP-EDECO-RESULT"},
		{"export -f refuses", "func d(c *Call) { c.Next(); }\n@d()\nfunction f() { :; }\nexport -f f\n", "cannot export"},
		{"agentic gate stays first", "func d(c *Call) { echo leaked; c.Next(); }\n@d()\nagentic func f() { :; }\nf()\n", "requires an explicit agentic"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runDecorated(t, tc.src)
			qt.Assert(t, qt.IsNotNil(err))
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr %q does not mention %q", stderr, tc.want)
			}
			qt.Assert(t, qt.IsFalse(strings.Contains(out, "leaked")))
		})
	}
}

func TestBashPPGoErrorDecoratorRegistration(t *testing.T) {
	t.Run("claimed marker is not a rung", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@go.error()
func f() int { return 7 }
v, err := f()
echo "v=$v err=[$err] status=$?"
`)
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "v=7 err=[] status=0\n"))
	})
	for _, tc := range []struct{ name, src, want string }{
		{"arguments refused", "@go.error(1)\nfunc f() int { return 1 }\n", "BASHPP-EDECO-GOERROR"},
		{"twice refused", "@go.error()\n@go.error()\nfunc f() int { return 1 }\n", "BASHPP-EDECO-GOERROR"},
		{"method refused", "type T int\n@go.error()\nfunc (v T) M() int { return 1 }\n", "BASHPP-EDECO-GOERROR"},
		{"generic refused", "@go.error()\nfunc f[T any](v T) T { return v }\n", "BASHPP-EDECO-GOERROR"},
		{"trailing error refused", "@go.error()\nfunc f() (int, error) { return 1, nil }\n", "BASHPP-EDECO-GOERROR"},
		{"other namespaced names stay reserved", "@go.retry()\nfunc f() int { return 1 }\n", "BASHPP-EDECO-RESERVED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, stderr, err := runDecorated(t, tc.src)
			qt.Assert(t, qt.IsNotNil(err))
			if !strings.Contains(stderr, tc.want) {
				t.Fatalf("stderr %q does not mention %q", stderr, tc.want)
			}
		})
	}
}

func TestBashPPGoErrorDecoratorStatusBoundary(t *testing.T) {
	// A shell exit status is 8-bit: a decorator-set c.Status wraps into 0..255
	// for both `$?` and the minted @go.error message, so a value that wraps to
	// zero is a nil error (a success). This pins the interpreter side; the
	// lowered parity harness (TestGoErrorDecoratorStatusBoundary) proves both
	// engines agree on the same boundary values.
	for _, tc := range []struct {
		name   string
		status int
		want   string
	}{
		{"zero is success", 0, "v=7 err=[] status=0\n"},
		{"max 8-bit is failure", 255, "v=7 err=[deploy: exit status 255] status=255\n"},
		{"out of range wraps to zero is success", 256, "v=7 err=[] status=0\n"},
		{"out of range wraps into range", 257, "v=7 err=[deploy: exit status 1] status=1\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := fmt.Sprintf(`func mark(c *Call) { c.Next(); c.Status = %d }
@mark()
@go.error()
func deploy() int { return 7 }
v, err := deploy()
echo "v=$v err=[$err] status=$?"
`, tc.status)
			out, stderr, err := runDecorated(t, src)
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

func TestBashPPDecoratorNative(t *testing.T) {
	var seen []string
	natives := map[string]interp.DecoratorFunc{
		"trace": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			seen = append(seen, "enter "+c.Name+" "+decoratorValues(c.Args))
			for _, a := range args {
				seen = append(seen, "arg "+a.Name+"="+a.Value)
			}
			c.Next(ctx)
			seen = append(seen, "leave "+c.Name+" status="+itoa(c.Status)+" results="+decoratorValues(c.Results))
			return nil
		},
		"deny": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			c.Status = 77
			return nil
		},
		"boom": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			return errBoom
		},
		"rewriteArgs": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			c.Args[0] = "9"
			c.Next(ctx)
			return nil
		},
		"addArg": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			c.Args = append(c.Args, "extra")
			c.Next(ctx)
			return nil
		},
		"rewriteResults": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			c.Next(ctx)
			c.Results[0] = "11"
			return nil
		},
	}
	t.Run("native wraps typed and shell functions", func(t *testing.T) {
		seen = nil
		out, stderr, err := runDecorated(t, `@trace(level: 2)
func pick(a int) int { return $a }
x := pick(5)
echo "x=$x"
@trace()
function sh() { return 3; }
sh a b
echo "status=$?"
`, interp.Decorators(natives))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "x=5\nstatus=3\n"))
		qt.Assert(t, qt.DeepEquals(seen, []string{
			"enter pick 5", "arg level=2", "leave pick status=0 results=5",
			"enter sh a,b", "leave sh status=3 results=",
		}))
	})
	t.Run("user function shadows a native", func(t *testing.T) {
		seen = nil
		out, _, err := runDecorated(t, "func trace(c *Call) { echo scripted; c.Next(); }\n@trace()\nfunc f() { echo body }\nf()\n", interp.Decorators(natives))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "scripted\nbody\n"))
		qt.Assert(t, qt.HasLen(seen, 0))
	})
	t.Run("native deny and error", func(t *testing.T) {
		out, _, err := runDecorated(t, "@deny()\nfunc f() { echo leaked }\nf()\necho \"status=$?\"\n", interp.Decorators(natives))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(out, "status=77\n"))
		out, stderr, err := runDecorated(t, "@boom()\nfunc f() { echo leaked }\nf()\n", interp.Decorators(natives))
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.Equals(out, ""))
		qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-EDECO-NATIVE")))
	})
	t.Run("mutated args feed Next and results feed the caller", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@rewriteArgs()
func arg(n int) int { return n }
@rewriteResults()
func result() int { return 3 }
a := arg(1)
b := result()
echo "$a $b"
`, interp.Decorators(natives))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "9 11\n"))
	})
	t.Run("mutated args feed shell positional parameters", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@rewriteArgs()
function sh() { echo "1=$1 n=$#"; }
sh 5 6
`, interp.Decorators(natives))
		qt.Assert(t, qt.IsNil(err))
		qt.Assert(t, qt.Equals(stderr, ""))
		qt.Assert(t, qt.Equals(out, "1=9 n=2\n"))
	})
	t.Run("mutated args are revalidated for arity", func(t *testing.T) {
		out, stderr, err := runDecorated(t, `@addArg()
func f(n int) { echo leaked }
f(1)
`, interp.Decorators(natives))
		qt.Assert(t, qt.IsNotNil(err))
		qt.Assert(t, qt.IsTrue(strings.Contains(stderr, "BASHPP-EDECO-ARG")))
		qt.Assert(t, qt.IsFalse(strings.Contains(out, "leaked")))
	})
}

// TestBashPPDecoratorAdvisedRestore pins that Call.Advised names the rung
// that is EXECUTING: after an inner Next returns, the outer rung sees its own
// rule id again, not the inner rung's.
func TestBashPPDecoratorAdvisedRestore(t *testing.T) {
	var log []string
	natives := map[string]interp.DecoratorFunc{
		"audit": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			log = append(log, "before="+c.Advised)
			c.Next(ctx)
			log = append(log, "after="+c.Advised)
			return nil
		},
	}
	advice := func(name, file string, agentic bool) []interp.DecoratorSpec {
		if name != "pay" {
			return nil
		}
		return []interp.DecoratorSpec{{ID: "r1", Name: "audit"}}
	}
	out, stderr, err := runDecorated(t, `func trace(c *Call) { a := c.Advised; echo "trace in <$a>"; c.Next(); a2 := c.Advised; echo "trace out <$a2>"; }
@trace()
func pay() { echo paying }
pay()
`, interp.Decorators(natives), interp.Advice(advice))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "trace in <>\npaying\ntrace out <>\n"))
	qt.Assert(t, qt.DeepEquals(log, []string{"before=r1", "after=r1"}))
}

// TestBashPPDecoratorTypedNoopPreservation drives a REAL decorated call end
// to end: a no-op decorator must leave pointer, map, channel, interface and
// function-valued arguments exactly as an undecorated call would, including
// mutations the body makes through them.
func TestBashPPDecoratorTypedNoopPreservation(t *testing.T) {
	const body = `	p.N = 7
	m := mv.(map[string]int)
	m["k"] = 9
	ch <- 3
	n := f()
	switch x := v.(type) {
	case int:
		echo "iface int $x"
	default:
		echo "iface lost"
	}
	return $n
`
	const drive = `func ten() int { return 10 }
func main() {
	b := Box{N: 1}
	p := &b
	m := map[string]int{"seed": 1}
	ch := make(chan int, 1)
	i := 42
	r := mutate(p, m, ch, i, ten)
	got := <-ch
	printf 'r=%s n=%s k=%s seed=%s got=%s\n' "$r" b.N m["k"] m["seed"] "$got"
}
main()
`
	sig := "func mutate(p *Box, mv any, ch chan int, v any, f func() int) int {\n"
	undecorated := "type Box struct { N int }\n" + sig + body + "}\n" + drive
	decorated := "func noop(c *Call) { c.Next(); }\ntype Box struct { N int }\n@noop()\n" + sig + body + "}\n" + drive

	wantOut, wantErr, err := runDecorated(t, undecorated)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(wantErr, ""))
	qt.Assert(t, qt.Equals(wantOut, "iface int 42\nr=10 n=7 k=9 seed=1 got=3\n"))

	out, stderr, err := runDecorated(t, decorated)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.Equals(out, wantOut))
}

// TestBashPPDecoratorManagerRegressions embeds the manager's exact source
// fixtures and expected output. These are end-to-end contracts: helper-only
// cell tests do not exercise result transport or the target/decorator unwind
// boundary.
func TestBashPPDecoratorManagerRegressions(t *testing.T) {
	for _, tc := range []struct{ name, src, want string }{
		{
			"identity.bpp",
			`#!/usr/bin/env bashy
# bash++ profile: decorators · phase S197 · status: planned
#
# Typed object identity through no-op decorator.

func noop(c *Call) {
    c.Next()
}

@noop()
func identity(ch chan string) chan string {
    return ch
}

func main() {
    c := make(chan string, 1)
    c <- "token"
    d := identity(c)
    v := <-d
    echo "identity:[$v]"
}
main()
`,
			"identity:[token]\n",
		},
		{
			"lifecycle.bpp",
			`#!/usr/bin/env bashy
# bash++ profile: decorators · phase S197 · status: planned
#
# defer/panic/recover lifecycle through decorator frames.

func catch(c *Call) {
    defer func() {
        r := recover()
        echo "caught:[$r]"
        c.Status = 1
    }()
    c.Next()
}

func cleanup() { echo "defer:body" }

@catch()
func panic_maker() {
    defer cleanup()
    panic("boom")
}

panic_maker()
echo "status:[$?]"
`,
			"defer:body\ncaught:[boom]\nstatus:[1]\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runDecorated(t, tc.src)
			qt.Assert(t, qt.IsNil(err))
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.Equals(out, tc.want))
		})
	}
}

func decoratorValues(values []any) string {
	parts := make([]string, len(values))
	for i, value := range values {
		parts[i] = fmt.Sprint(value)
	}
	return strings.Join(parts, ",")
}

func TestBashPPDecoratorAdvice(t *testing.T) {
	var log []string
	natives := map[string]interp.DecoratorFunc{
		"audit": func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
			log = append(log, "audit "+c.Name+" advised="+c.Advised)
			c.Next(ctx)
			return nil
		},
	}
	advice := func(name, file string, agentic bool) []interp.DecoratorSpec {
		if !strings.HasPrefix(name, "pay") {
			return nil
		}
		// Duplicated rule ids collapse to one application.
		return []interp.DecoratorSpec{{ID: "r1", Name: "audit"}, {ID: "r1", Name: "audit"}}
	}
	log = nil
	out, stderr, err := runDecorated(t, `func trace(c *Call) { echo "trace in"; c.Next(); echo "trace out"; }
@trace()
func pay() { echo paying }
function payout() { echo "paying out"; }
func other() { echo other }
pay()
payout
other()
eval 'payout() { echo "paying out again"; }'
payout
declare -f payout
`, interp.Decorators(natives), interp.Advice(advice))
	qt.Assert(t, qt.Equals(stderr, ""))
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.Equals(out, "trace in\npaying\ntrace out\npaying out\nother\npaying out again\npayout () \n{ \n    echo \"paying out again\"\n}\n"))
	// Advice is outermost: the audit entry precedes the source trace, and
	// re-registration via eval applied it exactly once more.
	qt.Assert(t, qt.DeepEquals(log, []string{"audit pay advised=r1", "audit payout advised=r1", "audit payout advised=r1"}))
	_, _, err = runDecorated(t, "function payout() { :; }\nexport -f payout\n", interp.Decorators(natives), interp.Advice(advice))
	qt.Assert(t, qt.IsNotNil(err))
}

var errBoom = errors.New("boom")

func itoa(n int) string { return strconv.Itoa(n) }

func TestBashPPDecoratorAdviceNativeOnly(t *testing.T) {
	for _, declaration := range []string{
		"func guard(c *Call) { echo bypass; c.Next(); }\n",
		"guard() { echo bypass; }\n",
	} {
		for _, registered := range []bool{false, true} {
			natives := map[string]interp.DecoratorFunc{}
			calls := 0
			if registered {
				natives["guard"] = func(ctx context.Context, c *interp.Call, args []interp.DecoratorArg) error {
					calls++
					c.Status = 77
					return nil
				}
			}
			advice := func(name, file string, agentic bool) []interp.DecoratorSpec {
				if name == "target" {
					return []interp.DecoratorSpec{{ID: "policy", Name: "guard"}}
				}
				return nil
			}
			out, stderr, err := runDecorated(t, declaration+"func target() { echo body }\ntarget()\n", interp.Decorators(natives), interp.Advice(advice))
			if out != "" || err == nil {
				t.Fatalf("policy bypass: registered=%v stdout=%q stderr=%q err=%v", registered, out, stderr, err)
			}
			if registered && (calls != 1 || stderr != "") {
				t.Fatalf("native guard: calls=%d stderr=%q", calls, stderr)
			}
			if !registered && !strings.Contains(stderr, "BASHPP-EDECO-UNDEF") {
				t.Fatalf("missing native: %q", stderr)
			}
		}
	}
}
