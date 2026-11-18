//go:build full

package lower_test

import "testing"

func TestPythonCallbacksInterpretedNativeParity(t *testing.T) {
	got := testPythonFenceInterpretedNativeParityAt(t, `~~~python as py
from collections.abc import Callable
def apply_twice(fn: Callable[[int], int], value: int) -> int:
    return fn(fn(value))
def ping(fn: Callable[[int], int], value: int) -> int:
    return fn(value)
~~~
func increment(value int) int { return value + 1 }
func pong(value int) int {
  if value >= 3 { return value }
  next := value + 1
  return py.ping(pong, next)
}
a := py.apply_twice(increment, 40)
offset := 2
closure := func(value int) int { return value + offset }
b := py.apply_twice(closure, 10)
c := py.ping(pong, 0)
echo "$a,$b,$c"
`, "input.bpp")
	if got != "42,14,3\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestPythonCallbacksConcurrentContextParity(t *testing.T) {
	got := testForeignParityArtifactAt(t, `~~~python as py
from collections.abc import Callable
def call(fn: Callable[[int], int]) -> int:
    return fn(1)
def twice(value: int) -> int:
    import time
    time.sleep(.02)
    return value * 2
~~~
func double(value int) int { return py.twice(value) }
func invoke() int { return py.call(double) }
func worker(ch chan int) { value := py.call(double); ch <- $value }
func main() {
ch := make(chan int)
go worker(ch)
go worker(ch)
a := <-ch
b := <-ch
echo "$a,$b"
}
main()
`, "input.bpp", `
func init() {
  results := make(chan int, 2)
  go func() { results <- invoke() }()
  go func() { results <- invoke() }()
  if <-results != 2 || <-results != 2 { panic("concurrent callback result") }
}
`, "-race")
	if got != "2,2\n" {
		t.Fatalf("output = %q", got)
	}
}
