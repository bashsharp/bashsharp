//go:build full

package lower_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/internal"
)

func TestPythonHandleIteratorLowered(t *testing.T) {
	source, err := os.ReadFile("../internal/testdata/python-iterators.bpp")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("../internal/testdata/python-iterators.stdout")
	if err != nil {
		t.Fatal(err)
	}
	if got := testPythonFenceInterpretedNativeParityAt(t, string(source), filepath.Join(t.TempDir(), "iterators.bpp")); got != string(want) {
		t.Fatalf("output=%q", got)
	}
}

func TestPythonHandleIteratorFailureWithoutOptIn(t *testing.T) {
	source := `~~~python as py
def bad():
    def values():
        yield 1
        raise ValueError('unhandled iteration')
    return values()
~~~
func main() {
 h, err := py.bad()
 for value := range h { echo "$value"; }
 echo unreachable
}
main()
`
	interpreted, native := foreignOutcomes(t, source)
	for name, got := range map[string]foreignOutcome{"interpreted": interpreted, "native": native} {
		if got.stdout != "1\n" || !strings.Contains(got.stderr, "ValueError: unhandled iteration") {
			t.Errorf("%s = %+q", name, got)
		}
	}
}

func TestPythonIteratorLowered(t *testing.T) {
	source := `~~~python
def items() -> Iterator[int]:
    yield 1
    yield 2
~~~
func main() {
 values := items()
 for value := range values { echo "$value"; }
}
main()
`
	out := testPythonFenceInterpretedNativeParityAt(t, source, "input.bpp")
	if out != "1\n2\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestPythonTextIOFilterLowered(t *testing.T) {
	internal.StreamTestConsumers(t)
	source := `~~~python as py
def upper(stdin: TextIO) -> Iterator[str]:
    for line in stdin:
        yield line.upper()
~~~
printf 'one\ntwo\n' | py.upper | cat
`
	out := testPythonFenceInterpretedNativeParityAt(t, source, "input.bpp")
	if out != "ONE\nTWO\n" {
		t.Fatalf("out=%q", out)
	}
}

func TestRustIteratorLowered(t *testing.T) {
	requireRustToolchain(t)
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~rust as rs
pub fn items() -> impl Iterator<Item = i64> { 1..3 }
~~~
func main() {
 values := rs.items()
 for value := range values { echo "$value"; }
}
main()
`, "input.bpp")
	if got != "1\n2\n" {
		t.Fatal(got)
	}
}

func TestPythonIteratorLexicalShadowLowered(t *testing.T) {
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~python
def items() -> Iterator[int]:
    yield 8
~~~
func one() {
 values := items()
 for value := range values { echo "$value"; }
}
func two() {
 values := make(chan int, 1)
 values <- 9
 close(values)
 for value := range values { echo "$value"; }
}
one()
two()
`, "input.bpp")
	if got != "8\n9\n" {
		t.Fatal(got)
	}
}

// Existing decorator phases remain in order around generated foreign adapters:
// a decorator can bind/transform inputs before Next; iteration completes and
// closes before the result/contract phase observes the result.
func TestForeignStreamingDecoratorParity(t *testing.T) {
	internal.StreamTestConsumers(t)
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~python as py
def items(n: int) -> Iterator[int]:
    for i in range(n): yield i
def upper(stdin: TextIO) -> Iterator[str]:
    for line in stdin: yield line.upper()
~~~
func observe(c *Call) {
 echo before
 c.Next()
 echo after
}
@observe()
@go.error()
func count(n int) int {
 values := py.items(n)
 total := 0
 for value := range values { total = total + 1; }
 return total
}
@observe()
func filter() {
 printf 'one\ntwo\n' | py.upper | cat
}
n, err := count(3)
echo "$n,$err"
filter()
`, "input.bpp")
	if got != "before\nafter\n3,\nbefore\nONE\nTWO\nafter\n" {
		t.Fatal(got)
	}
}

func TestPythonIteratorTypedArithmeticLowered(t *testing.T) {
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~python
def items() -> Iterator[int]:
    yield 2
    yield 3
~~~
func sum() int {
 values := items()
 total := 0
 for value := range values { total = total + value; }
 return total
}
n := sum()
echo "$n"
`, "input.bpp")
	if got != "5\n" {
		t.Fatal(got)
	}
}

func TestPythonIteratorBytesLowered(t *testing.T) {
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~python
def items() -> Iterator[bytes]:
    yield b'abc'
~~~
func main() {
 values := items()
 for value := range values { echo "$value"; }
}
main()
`, "input.bpp")
	if got == "" {
		t.Fatal("bytes disappeared")
	}
}

func TestRustIteratorObjectBytesLowered(t *testing.T) {
	requireRustToolchain(t)
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~rust as rs
use serde::Serialize;
#[derive(Serialize)]
struct Item { n:i64 }
pub fn bytes() -> impl Iterator<Item = Vec<u8>> { vec![vec![97u8,98,99]].into_iter() }
pub fn items() -> impl Iterator<Item = Item> { vec![Item{n:7}].into_iter() }
~~~
func main() {
 bytes := rs.bytes()
 for value := range bytes { echo "$value"; }
 items := rs.items()
 for value := range items { echo "$value"; }
}
main()
`, "input.bpp")
	if got != "\"YWJj\"\n{\"n\":7}\n" {
		t.Fatal(got)
	}
}
