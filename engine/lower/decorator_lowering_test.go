//go:build full

package lower_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// Decorated typed callables lower to a chain inside the private entry
// (dhnt/docs/bashpp-decorators-and-advice.md §5). Every case here is compiled
// for real, built with the Go toolchain, executed, and — through execute —
// diffed byte for byte against the interpreter running the same source, so
// each row is a measured parity claim, not a golden.

func TestDecoratedCallableExecution(t *testing.T) {
	for _, tc := range []struct{ name, src, out, err string }{
		{
			"stack order is outermost first with per-call args",
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
			"in x\nin y\nbody\nout y\nout x\n", "",
		},
		{
			"trace sees name and status",
			`func trace(c *Call) {
	name := c.Name
	echo "-> $name"
	c.Next()
	st := c.Status
	echo "<- $name status=$st"
}
@trace()
func deploy(env string) {
	echo "deploying $env"
	false
}
deploy(prod)
echo "status=$?"
`,
			"-> deploy\ndeploying prod\n<- deploy status=1\nstatus=1\n", "",
		},
		{
			"arguments evaluate per invocation in the declaration scope",
			`label := "outer"
func tag(c *Call, v string) { echo "tag $v"; c.Next(); }
@tag($label)
func f(label string) { echo "body $label"; }
f(inner)
n := 1
func num(c *Call, k int) { echo "num $k"; c.Next(); }
@num($n)
func g() { :; }
g()
n=2
g()
`,
			"tag outer\nbody inner\nnum 1\nnum 2\n", "",
		},
		{
			"repeated Next rebinds the body freshly and keeps the defer lifecycle",
			`func twice(c *Call) { c.Next(); c.Next(); echo "after"; }
func cleanup() { echo cleanup }
@twice()
func f(n int) { defer cleanup(); echo "n=$n"; n=9; }
f(3)
echo done
`,
			"n=3\ncleanup\nn=3\ncleanup\nafter\ndone\n", "",
		},
		{
			"results travel through the context and may be rewritten",
			`func trace(c *Call) {
	c.Next()
	r := c.Results[0]
	echo "saw $r"
}
@trace()
func pick(a, b int) int { return $b }
x := pick(1, 7)
echo "x=$x"
func upper(c *Call) {
	c.Next()
	c.Results = []any{"REWRITTEN"}
}
@upper()
func greet() string { return "hello" }
y := greet()
echo "y=$y"
`,
			"saw 7\nx=7\ny=REWRITTEN\n", "",
		},
		{
			"skip denies the body, yields zero results and the decorator's status",
			`func deny(c *Call) {
	echo "denied"
	c.Status = 3
}
@deny()
func secret() int { echo leaked; return 1 }
x := secret()
echo "x=<$x> status=$?"
@deny()
func pair(x int) (int, string) { return $((x + 1)), "y" }
p, q := pair(2)
echo "p=<$p> q=<$q> status=$?"
`,
			"denied\nx=<0> status=3\ndenied\np=<0> q=<> status=3\n", "",
		},
		{
			"args are the bound parameters and Caller is the calling frame",
			`func show(c *Call) {
	args := c.Args
	n := len(args)
	a := c.Args[0]
	b := c.Args[1]
	who := c.Caller
	echo "args=$a $b n=$n caller=$who"
	c.Next()
}
@show()
func f(a string, b int) { :; }
func outer() { f(y, 3) }
f(x, 2)
outer()
`,
			"args=x 2 n=2 caller=main\nargs=y 3 n=2 caller=outer\n", "",
		},
		{
			"args rewrite reaches the body on every Next",
			`func swap(c *Call) { c.Args = []any{"second", 20}; c.Next(); }
@swap()
func f(a string, b int) { echo "a=$a b=$b"; }
f(first, 10)
`,
			"a=second b=20\n", "",
		},
		{
			"keyword and default arguments with a retrying Next",
			`func retry(c *Call, n int = 1, backoff string = "0") {
	for i := 0; i < n; i++ {
		c.Next()
		st := c.Status
		if st == 0 { return }
	}
}
attempts := 0
@retry(n: 3)
func flaky() {
	attempts = attempts + 1
	echo "attempt $attempts"
	[ "$attempts" -ge 2 ]
}
flaky()
echo "status=$? attempts=$attempts"
`,
			"attempt 1\nattempt 2\nstatus=0 attempts=2\n", "",
		},
		{
			"method value, interface dispatch and direct call are all decorated",
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
			"trace T.Show\nshow\ntrace T.Show\nshow\ntrace T.Show\nshow\n", "",
		},
		{
			"function handle keeps the decoration",
			`func trace(c *Call) { echo "traced"; c.Next(); }
@trace()
func f() { echo body }
g := f
g()
`,
			"traced\nbody\n", "",
		},
		{
			"recursion re-enters the chain on every level",
			`func trace(c *Call) { n := c.Args[0]; echo "enter $n"; c.Next(); }
@trace()
func down(n int) {
	if n > 0 {
		down($((n - 1)))
	}
}
down(2)
`,
			"enter 2\nenter 1\nenter 0\n", "",
		},
		{
			"body defers run before Next returns",
			`func trace(c *Call) { c.Next(); echo "after next"; }
func cleanup() { echo cleanup }
@trace()
func f() { defer cleanup(); echo body }
f()
echo done
`,
			"body\ncleanup\nafter next\ndone\n", "",
		},
		{
			"the agentic gate precedes every decorator on a function and a method",
			`func trace(c *Call) { a := c.Agentic; echo "decorator ran agentic=$a"; c.Next(); }
@trace()
agentic func act() { echo acted }
type Counter int
@trace()
agentic func (c Counter) act(k int) { echo "act $k" }
var c Counter = 1
act()
echo "status=$?"
c.act(5)
echo "status=$?"
agentic { act(); c.act(6); }
`,
			"status=1\nstatus=1\ndecorator ran agentic=true\nacted\ndecorator ran agentic=true\nact 6\n",
			"act: agentic action requires an explicit agentic { ...; } scope\nact: agentic action requires an explicit agentic { ...; } scope\n",
		},
		{
			"typed channel identity survives the chain",
			`func pass(c *Call) { c.Next(); }
@pass()
func narrow(ch chan int) <-chan int {
	ch <- 9
	return ch
}
func recv(ch <-chan int) {
	v := <-ch
	println(v)
}
func main() {
	ch := make(chan int, 2)
	read := narrow(ch)
	recv(read)
	ch <- 10
	recv(read)
}
main()
`,
			"9\n10\n", "",
		},
		{
			"map identity survives the chain",
			`func pass(c *Call) { c.Next(); }
type Bag map[string]int
@pass()
func fill(m Bag) Bag {
	m["k"] = 7
	return m
}
func main() {
	m := Bag{}
	out := fill(m)
	w := out["k"]
	x := m["k"]
	println(w, x)
}
main()
`,
			"7 7\n", "",
		},
		{
			"generic target",
			`func trace(c *Call) { a := c.Args[0]; echo "trace $a"; c.Next(); r := c.Results[0]; echo "result $r"; }
@trace()
func id[T any](v T) T { return v }
x := id(7)
y := id("s")
echo "$x $y"
`,
			"trace 7\nresult 7\ntrace s\nresult s\n7 s\n", "",
		},
		{
			"named results settle through the chain",
			`func trace(c *Call) { c.Next(); a := c.Results[0]; b := c.Results[1]; echo "saw $a $b"; }
@trace()
func named(x int) (n int, s string) {
	n = x
	s = "str"
	return
}
n, s := named(1)
echo "n=$n s=$s"
`,
			"saw 1 str\nn=1 s=str\n", "",
		},
		{
			"an undefined decorator fails the call at status 1",
			`@missing(1)
func f() { echo body; }
f()
echo "status=$?"
`,
			"status=1\n", "BASHPP-EDECO-UNDEF: decorator missing is not defined\n",
		},
		{
			"argument and result rewrites are revalidated",
			`func bad(c *Call) { c.Args = []any{"x"}; c.Next(); }
@bad()
func f(n int) { echo "n=$n"; }
f(1)
echo "status=$?"
func count(c *Call) { c.Args = []any{1, 2}; c.Next(); }
@count()
func g(n int) { echo "n=$n"; }
g(1)
echo "status=$?"
func res(c *Call) { c.Next(); c.Results = []any{"str"}; }
@res()
func h() int { return 1 }
x := h()
echo "x=$x status=$?"
func many(c *Call) { c.Next(); c.Results = []any{1, 2}; }
@many()
func k() int { return 1 }
y := k()
echo "y=$y status=$?"
`,
			"status=1\nstatus=1\nx= status=2\ny= status=2\n",
			"BASHPP-EDECO-ARG: f: cannot use \"x\" as int value for parameter n\n" +
				"BASHPP-EDECO-ARG: g: decorator supplied 2 argument(s); expected 1\n" +
				"BASHPP-EDECO-RESULT: h: cannot use \"str\" as int result 1\n" +
				"assignment mismatch: 1 variable(s) but 0 value(s)\n" +
				"BASHPP-EDECO-RESULT: k declares 1 result(s); decorator supplied 2\n" +
				"assignment mismatch: 1 variable(s) but 0 value(s)\n",
		},
		{
			"a user Call type shadows the predeclared one",
			`type Call struct { Name string }
func mine(c *Call) { n := c.Name; echo "mine $n"; }
func main() {
	v := Call{Name: "shadowed"}
	p := &v
	mine(p)
}
main()
`,
			"mine shadowed\n", "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := compile(t, tc.src)
			out, stderr := execute(t, r)
			if out != tc.out || stderr != tc.err {
				t.Fatalf("out=%q stderr=%q\nwant out=%q stderr=%q\n%s", out, stderr, tc.out, tc.err, r.Source)
			}
		})
	}
}

// buildAndRun builds a compiled unit and runs it without the interpreter
// parity diff, for shapes the interpreter does not evaluate natively yet.
func buildAndRun(t *testing.T, r compiledCase) (string, string, int) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "generated.go"), r.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(this)))
	module := "module lowerfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "program")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-o", binary, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, out, r.Source)
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/no-tools"}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	status := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = exit.ExitCode()
	}
	return out.String(), stderr.String(), status
}

// A variadic tail is one context position per element, rebound from the
// context on every Next. The interpreter does not yet evaluate len() over a
// variadic parameter, so this row is compiled-only.
func TestDecoratedVariadicRebinding(t *testing.T) {
	r := compile(t, `func grow(c *Call) { c.Args = []any{"h", 1, 9}; c.Next(); }
@grow()
func v(head string, rest ...int) { n := len(rest); last := rest[n-1]; echo "$head n=$n last=$last"; }
v(h, 1)
func noopd(c *Call) { c.Next(); }
@noopd()
func w(head string, rest ...int) { n := len(rest); last := rest[n-1]; echo "$head n=$n last=$last"; }
w(a, 1, 2)
func shrink(c *Call) { c.Args = []any{"only"}; c.Next(); }
@shrink()
func x(head string, rest ...int) { n := len(rest); echo "$head n=$n"; }
x(a, 1, 2)
`)
	out, stderr, status := buildAndRun(t, r)
	if out != "h n=2 last=9\na n=2 last=2\nonly n=0\n" || stderr != "" || status != 0 {
		t.Fatalf("out=%q stderr=%q status=%d", out, stderr, status)
	}
}

// The chain is inside the private entry, and the public symbol keeps its
// declared signature: a Go caller of the public wrapper is decorated too, and
// the generated wrapper mentions no Call.
func TestDecoratorChainLivesInPrivateEntry(t *testing.T) {
	r := compile(t, `func trace(c *Call) { echo traced; c.Next(); }
@trace()
func add(a int, b int) int { return $((a + b)) }
x := add(1, 2)
echo "x=$x"
`)
	source := string(r.Source)
	private := source[strings.Index(source, "func __bpp0_call_add("):]
	private = private[:strings.Index(private, "\nfunc add(")]
	for _, want := range []string{"__bpp0_rt.Call{Name: \"add\"", ".Decorate(", "__bpp0_decorators_add(", "DecoratedArg[int]", "DecoratedResult[int]"} {
		if !strings.Contains(private, want) {
			t.Fatalf("private entry lacks %q:\n%s", want, private)
		}
	}
	public := source[strings.Index(source, "\nfunc add("):]
	public = public[:strings.Index(public, "\n}\n")+3]
	if !strings.HasPrefix(public, "\nfunc add(a int, b int) int {") || strings.Contains(public, "Call") || strings.Contains(public, "Decorat") {
		t.Fatalf("public wrapper changed shape:\n%s", public)
	}
	if !strings.Contains(source, "type Call = __bpp0_rt.Call\n") {
		t.Fatalf("predeclared Call alias missing:\n%s", source)
	}
	if out, stderr := execute(t, r); out != "traced\nx=3\n" || stderr != "" {
		t.Fatalf("out=%q stderr=%q", out, stderr)
	}

	// The alias is emitted only for a unit that names the predeclared Call.
	plain := compile(t, `func add(a int, b int) int { return $((a + b)) }
agentic func act() { echo acted }
x := add(1, 2)
echo "x=$x"
`)
	if strings.Contains(string(plain.Source), "Call") || strings.Contains(string(plain.Source), "Decorat") {
		t.Fatalf("undecorated unit mentions the decorator runtime:\n%s", plain.Source)
	}
}

// Native decorators resolve through the process-level shellrt.Decorators
// slot when a rung names no unit function; a unit function shadows a native
// one of the same name; a native error and a missing name are the engine's
// diagnostics. The unit is linked into a host that wires the slot, built and
// executed for real.
func TestDecoratorNativeSlot(t *testing.T) {
	const source = `n := 2
@native_trace("pos", level: 1)
@retry(n: $n)
func work(x int) int { echo "work $x"; return $((x * 2)) }
func retry(c *Call, n int) {
	for i := 0; i < n; i++ {
		c.Next()
	}
}
@fails()
func doomed() { echo unreachable }
@absent()
func lonely() { echo unreachable }
r := work(21)
echo "r=$r"
doomed()
echo "status=$?"
lonely()
echo "status=$?"
`
	const host = `package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"lowerfixture/generated"
	rt "github.com/bashsharp/bashsharp/engine/lower/shellrt"
)

func main() {
	rt.Decorators = map[string]rt.DecoratorFunc{
		"native_trace": func(ctx context.Context, c *rt.Call, args []rt.DecoratorArg) error {
			fmt.Printf("native -> %s caller=%s args=%v decorator-args=%v\n", c.Name, c.Caller, c.Args, args)
			c.Next()
			fmt.Printf("native <- %s status=%d results=%v\n", c.Name, c.Status, c.Results)
			c.Results = []any{c.Results[0].(int) + 1}
			return nil
		},
		"retry": func(ctx context.Context, c *rt.Call, args []rt.DecoratorArg) error {
			fmt.Println("native retry must be shadowed by the unit's retry")
			return nil
		},
		"fails": func(ctx context.Context, c *rt.Call, args []rt.DecoratorArg) error {
			return errors.New("refused")
		},
	}
	status, err := generated.Execute(rt.WithStdio(os.Stdin, os.Stdout, os.Stderr))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(status)
}
`
	r, err := lower.Compile(parse(t, source, "input.bpp"), lower.Options{Package: "generated", Entry: "Execute"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "generated"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "generated", "generated.go"), r.Source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(host), 0o600); err != nil {
		t.Fatal(err)
	}
	_, this, _, _ := runtime.Caller(0)
	root := filepath.Dir(filepath.Dir(filepath.Dir(this)))
	module := "module lowerfixture\n\ngo " + strings.TrimPrefix(runtime.Version(), "go") + "\n\nrequire github.com/bashsharp/bashsharp v0.0.0\nreplace github.com/bashsharp/bashsharp => " + root + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(dir, "program")
	build := exec.CommandContext(ctx, filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-mod=mod", "-o", binary, ".")
	build.Dir = dir
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s\n%s", err, out, r.Source)
	}
	cmd := exec.CommandContext(ctx, binary)
	cmd.Dir = t.TempDir()
	cmd.Env = []string{"PATH=/no-tools"}
	var out, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &stderr
	status := 0
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatal(err)
		}
		status = exit.ExitCode()
	}
	wantOut := "native -> work caller=main args=[21] decorator-args=[{ pos} {level 1}]\n" +
		"work 21\nwork 21\n" +
		"native <- work status=0 results=[42]\n" +
		"r=43\n" +
		"status=1\n" +
		"status=1\n"
	wantErr := "BASHPP-EDECO-NATIVE: @fails: refused\nBASHPP-EDECO-UNDEF: decorator absent is not defined\n"
	if out.String() != wantOut || stderr.String() != wantErr || status != 0 {
		t.Fatalf("out=%q stderr=%q status=%d\nwant out=%q stderr=%q status=0\n%s", out.String(), stderr.String(), status, wantOut, wantErr, r.Source)
	}
}

// Static diagnostics: the engine refuses these at registration or at the
// first call; lowering resolves every callee statically and refuses them at
// compile time.
func TestDecoratorStaticDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, src, code, msg string }{
		{"reserved namespace", "@pkg.trace()\nfunc f() { :; }\n", "BASHPP-EDECO-RESERVED", "@pkg.trace: namespaced decorators are reserved"},
		{"self decoration", "@f()\nfunc f(c *Call) { :; }\n", "BASHPP-EDECO-SELF", "f cannot decorate itself"},
		{"typed function without *Call", "func plain(x int) { :; }\n@plain()\nfunc f() { :; }\n", "BASHPP-EDECO-SIG", "plain is not a decorator: its first parameter must be *Call"},
		{"shell function", "sh() { :; }\n@sh()\nfunc f() { :; }\n", "BASHPP-EDECO-SIG", "sh is not a decorator: a shell function has no *Call parameter"},
		{"shadowed Call is not a decorator", "type Call struct { Name string }\nfunc mine(c *Call) { :; }\n@mine()\nfunc f() { :; }\n", "BASHPP-EDECO-SIG", "mine is not a decorator: its first parameter must be *Call"},
		{"cycle", "func a(c *Call) { c.Next(); }\n@a()\nfunc b(c *Call) { c.Next(); }\n@b()\nfunc a2(c *Call) { c.Next(); }\n@a2()\nfunc a(c *Call) { c.Next(); }\n", "BASHPP-EDECO-CYCLE", "is already decorating an active call"},
		{"decorated shell function", "func trace(c *Call) { c.Next(); }\n@trace()\nfunction backup() { :; }\n", lower.CodeUnsupported, "decorated shell functions are not lowered"},
		{"generic decorator", "func g[T any](c *Call) { c.Next(); }\n@g()\nfunc f() { :; }\n", lower.CodeUnsupported, "generic decorators are not lowered"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.src), "input.bpp")
			if err != nil {
				t.Fatal(err)
			}
			_, err = lower.Compile(f, lower.Options{})
			if err == nil {
				t.Fatal("compiled")
			}
			var list lower.ErrorList
			if !errors.As(err, &list) || len(list) == 0 {
				t.Fatalf("unexpected error shape: %v", err)
			}
			if list[0].Code != tc.code || !strings.Contains(list[0].Msg, tc.msg) {
				t.Fatalf("got %s %q, want %s %q", list[0].Code, list[0].Msg, tc.code, tc.msg)
			}
		})
	}
}
