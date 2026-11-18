//go:build full

package interp_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/internal"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestPythonIteratorLanguage(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		fail             bool
	}{
		{"empty", "    if False: yield 0", "", false},
		{"many", "    for n in range(1000): yield n", "1000\n", false},
		{"partial-error", "    yield 3\n    raise ValueError('after-value')", "3\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop := "for value := range values { echo \"$value\"; }"
			if tc.name == "many" {
				loop = "count := 0\nfor value := range values { count = count + 1; }\necho \"$count\""
			}
			source := "~~~python\ndef items() -> Iterator[int]:\n" + tc.body + "\n~~~\nfunc main() {\nvalues := items()\n" + loop + "\n}\nmain()\n"
			out, stderr, err := runPolyglot(t, source)
			if out != tc.want || (err != nil) != tc.fail {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
			if tc.fail && !strings.Contains(stderr, "after-value") {
				t.Fatal(stderr)
			}
		})
	}
}

func TestPythonIteratorPipeline(t *testing.T) {
	internal.StreamTestConsumers(t)
	out, stderr, err := runPolyglot(t, `~~~python as py
def values() -> Iterator[int]:
    yield 1
    yield 2
~~~
py.values | cat
`)
	if err != nil || stderr != "" || out != "1\n2\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestPythonTextIOFilterLanguage(t *testing.T) {
	internal.StreamTestConsumers(t)
	out, stderr, err := runPolyglot(t, `~~~python as py
def upper(stdin: TextIO) -> Iterator[str]:
    for line in stdin:
        yield line.upper()
~~~
printf 'one\ntwo\n' | py.upper | cat
`)
	if err != nil || stderr != "" || out != "ONE\nTWO\n" {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestPythonTextIOFilterMissingConsumer(t *testing.T) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(`~~~python as py
def upper(stdin: TextIO) -> Iterator[str]:
    try:
        for line in stdin: yield line.upper()
    finally:
        state.closed = True
def state() -> bool:
    return getattr(state,'closed',False)
~~~
printf 'one\ntwo\n' | py.upper | s221_nonexistent_pipeline_consumer
code=$?
closed := py.state()
echo "$code:$closed"
`), "missing-consumer.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var stdout, stderr strings.Builder
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &stdout, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	if err := runner.Run(ctx, file); err != nil || stdout.String() != "127:true\n" || !strings.Contains(stderr.String(), "s221_nonexistent_pipeline_consumer") {
		t.Fatalf("out=%q stderr=%q err=%v", stdout.String(), stderr.String(), err)
	}
}

// CPython v3.14.0, Lib/test/test_generators.py GeneratorTest.test_issue103488
// (PSF-2.0): the original generator body is `yield; raise ValueError()`.
// Preserve that body; add only the bridge's declared Iterator[Any] annotation.
func TestPythonIteratorOriginalGeneratorFailure(t *testing.T) {
	out, stderr, err := runPolyglot(t, `~~~python
def gen_raises() -> Iterator[Any]:
    yield
    raise ValueError()
~~~
func main() {
 values := gen_raises()
 for value := range values { echo seen; }
}
main()
`)
	if out != "seen\n" || err == nil || !strings.Contains(stderr, "ValueError") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestPythonIteratorEarlyBreak(t *testing.T) {
	out, stderr, err := runPolyglot(t, `~~~python
def forever() -> Iterator[int]:
    while True:
        yield 42
~~~
func main() {
 values := forever()
 for value := range values { echo "$value"; break; }
 echo done
}
main()
`)
	if out != "42\ndone\n" || stderr != "" || err != nil {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestPythonIteratorPersistentState(t *testing.T) {
	out, stderr, err := runPolyglot(t, `~~~python as py
def state(value: int = -1) -> int:
    if value >= 0: state.value=value
    return getattr(state,'value',0)
def items() -> Iterator[int]:
    try:
        while True: yield state()
    finally: state(99)
~~~
func main() {
 py.state(7)
 values := py.items()
 for value := range values { echo "$value"; break; }
 after := py.state()
 echo "$after"
}
main()
`)
	if out != "7\n99\n" || stderr != "" || err != nil {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestRustIteratorLanguage(t *testing.T) {
	requireRustToolchain(t)
	for _, tc := range []struct {
		name, body, want string
		fail             bool
	}{
		{"empty", "std::iter::empty()", "", false},
		{"many", "(0..1000).map(Ok)", "1000\n", false},
		{"partial-error", "vec![Ok(3), Err(\"after-value\".to_string())].into_iter()", "3\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			loop := "for value := range values { echo \"$value\"; }"
			if tc.name == "many" {
				loop = "count := 0\nfor value := range values { count = count + 1; }\necho \"$count\""
			}
			out, stderr, err := runPolyglot(t, "~~~rust as rs\npub fn items() -> impl Iterator<Item = Result<i64, String>> { "+tc.body+" }\n~~~\nfunc main() {\nvalues := rs.items()\n"+loop+"\n}\nmain()\n")
			if out != tc.want || (err != nil) != tc.fail {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
			if tc.fail && !strings.Contains(stderr, "after-value") {
				t.Fatal(stderr)
			}
		})
	}
}

func TestPythonTextIOFilterLargeAndEarlyExit(t *testing.T) {
	internal.StreamTestConsumers(t)
	for _, tc := range []struct{ name, body, tail, want string }{
		{"large", "    for i in range(2000): yield 'x'*100+'\\n'", "wc -l", "2000"},
		{"early", "    while True: yield 'first\\n'", "head -n 1", "first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runPolyglot(t, "~~~python as py\ndef output(stdin: TextIO) -> Iterator[str]:\n"+tc.body+"\n~~~\nprintf '' | py.output | "+tc.tail+"\n")
			if strings.TrimSpace(out) != tc.want || err != nil {
				t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
			}
		})
	}
}

func TestRustIteratorDropPreservesState(t *testing.T) {
	requireRustToolchain(t)
	out, stderr, err := runPolyglot(t, `~~~rust as rs
use std::sync::atomic::{AtomicI64,Ordering};
static DROPS:AtomicI64=AtomicI64::new(0);
struct Numbers;
impl Iterator for Numbers {
 type Item=i64;
 fn next(&mut self)->Option<i64>{Some(42)}
}
impl Drop for Numbers {fn drop(&mut self){DROPS.fetch_add(1,Ordering::SeqCst);}}
pub fn items()->impl Iterator<Item = i64>{Numbers}
pub fn drops()->i64{DROPS.load(Ordering::SeqCst)}
~~~
func main() {
 values := rs.items()
 for value := range values { echo "$value"; break; }
 n := rs.drops()
 echo "$n"
}
main()
`)
	if out != "42\n1\n" || stderr != "" || err != nil {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestPythonIteratorCloseError(t *testing.T) {
	out, stderr, err := runPolyglot(t, `~~~python
def values() -> Iterator[int]:
    try:
        while True: yield 1
    finally: raise ValueError('close-failed')
~~~
func main() {
 items := values()
 for value := range items { echo "$value"; break; }
}
main()
`)
	if out != "1\n" || err == nil || !strings.Contains(stderr, "close-failed") {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}

func TestPythonTextIOFilterPrintRouting(t *testing.T) {
	internal.StreamTestConsumers(t)
	out, stderr, err := runPolyglot(t, `~~~python as py
def output(stdin: TextIO) -> Iterator[str]:
    print('before')
    yield 'one\n'
    print('between')
    yield 'two\n'
    print('finally')
~~~
printf '' | py.output | cat
`)
	if out != "before\none\nbetween\ntwo\nfinally\n" || stderr != "" || err != nil {
		t.Fatalf("out=%q stderr=%q err=%v", out, stderr, err)
	}
}
