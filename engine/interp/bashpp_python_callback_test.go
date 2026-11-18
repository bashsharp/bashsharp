//go:build full

package interp_test

import (
	"strings"
	"testing"
)

const pythonCallbackFence = `~~~python as py
from collections.abc import Callable
def apply_twice(fn: Callable[[int], int], value: int) -> int:
    return fn(fn(value))
def call_once(fn: Callable[[int], int], value: int) -> int:
    return fn(value)
def noisy(fn: Callable[[], None]) -> None:
    print("before")
    import sys
    print("warn", file=sys.stderr)
    fn()
    print("after")
def ping(fn: Callable[[int], int], value: int) -> int:
    return fn(value)
def catch_status(fn: Callable[[int], int], value: int) -> str:
    try: fn(value)
    except BashCallbackError as exc: return str(exc.status)
    return "none"
~~~
`

func TestBashPPPythonCallbacksNamedClosureReentryAndOutput(t *testing.T) {
	out, diagnostic, err := runPolyglot(t, pythonCallbackFence+`
func increment(value int) int { return value + 1 }
func speak() { echo callback }
func pong(value int) int {
  if value >= 3 { return value }
  next := value + 1
  return py.ping(pong, next)
}
named := py.apply_twice(increment, 40)
offset := 2
closure := func(value int) int { return value + offset }
closed := py.apply_twice(closure, 10)
echo "$named,$closed"
py.noisy(speak)
deep := py.ping(pong, 0)
echo "deep=$deep"
`)
	if err != nil || out != "42,14\nbefore\ncallback\nafter\ndeep=3\n" || diagnostic != "warn\n" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

func TestBashPPPythonCallbackFailuresAndOptionalError(t *testing.T) {
	out, diagnostic, err := runPolyglot(t, pythonCallbackFence+`
func failing(value int) int { exit 3 }
func boom(value int) int { panic("callback exploded") }
a, first := py.call_once(failing, 1)
echo "a=$a first=$first"
status := py.catch_status(failing, 1)
echo "caught-status=$status"
b, second := py.call_once(boom, 1)
echo "b=$b second=$second"
good := func(value int) int { return value + 1 }
ok := py.call_once(good, 1)
echo "ok=$ok"
`)
	if err != nil || out != "a=0 first=BashCallbackError: bash++: shell callback failing failed (status 3)\ncaught-status=3\nb=0 second=BashCallbackError: bash++: shell callback boom failed (status 2)\nok=2\n" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	if !strings.Contains(diagnostic, "panic: callback exploded") {
		t.Fatalf("diagnostic=%q", diagnostic)
	}
}
