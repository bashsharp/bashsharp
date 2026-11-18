package polyglot

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const pythonCallbackFence = `
from collections.abc import Callable
import threading

kept = None
def zero(fn: Callable[[], int]) -> int:
    return 40
def once(fn: Callable[[int], int]) -> int:
    return fn(21)
def twice(fn: Callable[[int], int]) -> int:
    return fn(fn(10))
def values(fn: Callable[[object], object], value: object) -> object:
    return fn(value)
def keep(fn: Callable[[], int]) -> None:
    global kept
    kept = fn
def call_kept() -> str:
    try: kept()
    except BashCallbackError as exc: return type(exc).__name__ + ":" + str(exc)
    return "called"
def catch(fn: Callable[[], int]) -> str:
    try: fn()
    except BashCallbackError as exc: return type(exc).__name__ + ":" + str(exc)
    return "ok"
def after(fn: Callable[[], int]) -> int:
    fn()
    raise ValueError("after callback")
def ping(fn: Callable[[int], int], n: int) -> int:
    return fn(n)
def noisy(fn: Callable[[], int]) -> None:
    print("before")
    import sys
    print("warn", file=sys.stderr)
    fn()
    print("after")
def threaded(fn: Callable[[], int]) -> str:
    result = []
    def run():
        try: fn()
        except Exception as exc: result.append(type(exc).__name__ + ":" + str(exc))
    thread = threading.Thread(target=run)
    thread.start(); thread.join()
    return result[0]
def object_value() -> object:
    return object()
def pass_object(fn: Callable[[object], object], value: object) -> bool:
    return fn(value) is value
def nap(fn: Callable[[], int]) -> None:
    fn()
`

func pythonCallbackModule(t *testing.T) *Module {
	t.Helper()
	plan := pythonPlan(t, pythonCallbackFence)
	m := Start(plan, Python{})
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func TestPythonCallbackAnalysis(t *testing.T) {
	plan := pythonPlan(t, `
from typing import Callable
from collections.abc import Callable as ABCCallable
def a(fn: Callable[[int], int]) -> int: return fn(1)
def b(fn: ABCCallable[[], str]) -> str: return fn()
`)
	want := []string{"callback"}
	if !reflect.DeepEqual(plan.Exports[0].Signature.Params, want) || !reflect.DeepEqual(plan.Exports[1].Signature.Params, want) {
		t.Fatalf("callback signatures = %#v, %#v", plan.Exports[0].Signature, plan.Exports[1].Signature)
	}
	for _, annotation := range []string{"bashpp.RetainedCallback", "RetainedCallback"} {
		_, err := (Python{}).Analyze(t.Context(), "def bad(fn: "+annotation+") -> int: return 1\n")
		if err == nil || !strings.Contains(err.Error(), "retained callbacks are not supported") {
			t.Fatalf("%s error = %v", annotation, err)
		}
	}
}

func TestPythonCallbacksCallsValuesExpiryAndFailures(t *testing.T) {
	m := pythonCallbackModule(t)
	ctx := context.Background()
	if got, err := m.Call(ctx, "zero", func() int { return 1 }); err != nil || got.Value != int64(40) {
		t.Fatalf("zero = %#v, %v", got, err)
	}
	if got, err := m.Call(ctx, "once", func(v int64) int64 { return v * 2 }); err != nil || got.Value != int64(42) {
		t.Fatalf("once = %#v, %v", got, err)
	}
	if got, err := m.Call(ctx, "twice", func(v int64) int64 { return v + 1 }); err != nil || got.Value != int64(12) {
		t.Fatalf("twice = %#v, %v", got, err)
	}
	for _, value := range []any{nil, true, int64(3), 2.5, "text", []byte{0, 1}, []any{int64(1), "x"}, map[string]any{"x": int64(2)}} {
		got, err := m.Call(ctx, "values", Callback{Name: "echo", Invoke: func(context.Context, []any) (any, error) { return value, nil }}, value)
		if err != nil || !reflect.DeepEqual(got.Value, value) {
			t.Fatalf("value %#v = %#v, %v", value, got.Value, err)
		}
	}
	kept := Callback{Name: "kept", Invoke: func(context.Context, []any) (any, error) { return int64(1), nil }}
	if _, err := m.Call(ctx, "keep", kept); err != nil {
		t.Fatal(err)
	}
	got, err := m.Call(ctx, "call_kept")
	if err != nil || !strings.Contains(got.Value.(string), "BashCallbackError") || !strings.Contains(got.Value.(string), "expired") {
		t.Fatalf("expired = %#v, %v", got, err)
	}
	failing := Callback{Name: "failing", Invoke: func(context.Context, []any) (any, error) { return nil, errors.New("callback says no") }}
	got, err = m.Call(ctx, "catch", failing)
	if err != nil || !strings.Contains(got.Value.(string), "BashCallbackError:callback says no") {
		t.Fatalf("caught = %#v, %v", got, err)
	}
	if _, err := m.Call(ctx, "once", failing); err == nil {
		t.Fatal("uncaught callback failure succeeded")
	} else if detail, ok := ForeignErrorDetail(err); !ok || detail.Code != "BashCallbackError" {
		t.Fatalf("detail = %#v, %v", detail, err)
	}
	noop := Callback{Name: "noop", Invoke: func(context.Context, []any) (any, error) { return nil, nil }}
	if _, err := m.Call(ctx, "after", noop); err == nil {
		t.Fatal("later Python exception succeeded")
	} else if detail, ok := ForeignErrorDetail(err); !ok || detail.Code != "ValueError" {
		t.Fatalf("later detail = %#v, %v", detail, err)
	}
	got, err = m.Call(ctx, "threaded", noop)
	if err != nil || !strings.Contains(got.Value.(string), "BashCallbackError") || !strings.Contains(got.Value.(string), "calling thread") {
		t.Fatalf("threaded = %#v, %v", got, err)
	}
}

func TestPythonCallbacksHandlesReentryAndOrdering(t *testing.T) {
	m := pythonCallbackModule(t)
	ctx := context.Background()
	object, err := m.Call(ctx, "object_value")
	if err != nil {
		t.Fatal(err)
	}
	handle := object.Value.(*Handle)
	echo := Callback{Name: "echo", Invoke: func(_ context.Context, args []any) (any, error) { return args[0], nil }}
	if got, err := m.Call(ctx, "pass_object", echo, handle); err != nil || got.Value != true {
		t.Fatalf("handle echo = %#v, %v", got, err)
	}
	foreign := Start(m.plan, Python{})
	defer foreign.Close()
	bad := Callback{Name: "foreign", Invoke: func(context.Context, []any) (any, error) {
		return &Handle{module: foreign, generation: foreign.generation, ID: 1}, nil
	}}
	if got, err := m.Call(ctx, "catch", bad); err != nil || !strings.Contains(got.Value.(string), "stale or foreign") {
		t.Fatalf("foreign = %#v, %v", got, err)
	}
	stale := Callback{Name: "stale", Invoke: func(context.Context, []any) (any, error) {
		return &Handle{module: m, generation: m.generation - 1, ID: handle.ID}, nil
	}}
	if got, err := m.Call(ctx, "catch", stale); err != nil || !strings.Contains(got.Value.(string), "worker is gone") {
		t.Fatalf("stale = %#v, %v", got, err)
	}
	var depth, deepest int32
	var recurse Callback
	recurse = Callback{Name: "recurse", Invoke: func(ctx context.Context, args []any) (any, error) {
		d := atomic.AddInt32(&depth, 1)
		defer atomic.AddInt32(&depth, -1)
		if d > atomic.LoadInt32(&deepest) {
			atomic.StoreInt32(&deepest, d)
		}
		got, err := m.Call(ctx, "ping", recurse, args[0].(int64)+1)
		return got.Value, err
	}}
	if _, err := m.Call(ctx, "ping", recurse, int64(0)); err == nil || !strings.Contains(err.Error(), "exceeds the bound of 8") {
		t.Fatalf("depth error = %v", err)
	}
	if deepest != MaxCallbackDepth {
		t.Fatalf("deepest = %d", deepest)
	}
	var transcript strings.Builder
	speaker := Callback{Name: "speaker", Invoke: func(context.Context, []any) (any, error) { transcript.WriteString("callback\n"); return nil, nil }}
	m.SetCallbacks(Callbacks{Output: func(out, err string) { transcript.WriteString(out); transcript.WriteString(err) }})
	result, err := m.Call(ctx, "noisy", speaker)
	transcript.WriteString(result.Stdout)
	transcript.WriteString(result.Stderr)
	if err != nil || transcript.String() != "before\nwarn\ncallback\nafter\n" {
		t.Fatalf("ordering = %q, %#v, %v", transcript.String(), result, err)
	}
}

func TestPythonCallbackCancellation(t *testing.T) {
	m := pythonCallbackModule(t)
	blocking := Callback{Name: "blocking", Invoke: func(ctx context.Context, _ []any) (any, error) { <-ctx.Done(); return nil, ctx.Err() }}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := m.Call(ctx, "nap", blocking); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel = %v", err)
	}
	if m.alive() {
		t.Fatal("worker survived cancellation")
	}
}
