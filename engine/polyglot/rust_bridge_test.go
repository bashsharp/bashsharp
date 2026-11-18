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

// Source-derived fixtures for the advanced Rust bridge (Sprint 221 story
// #577, B11 shell callbacks and B12 opaque handles). Every case below ports an
// exact upstream test or documented example, expressed against the Bash#
// boundary rather than copying the upstream harness:
//
//   - rust-lang/rust 1.95.0, commit 59807616e1fa2540724bfbac14d7976d7e4a3860,
//     "MIT OR Apache-2.0" (library/std/Cargo.toml `license`):
//     library/alloctests/tests/rc.rs `test_live`, `test_dead`, `try_unwrap`
//     (ownership/drop: a live handle upgrades, a dropped one refuses, an
//     unwrap fails while another owner exists);
//     library/coretests/tests/cell.rs `double_imm_borrow`,
//     `no_mut_then_imm_borrow`, `no_imm_then_borrow_mut`,
//     `imm_release_borrow_mut`, `mut_release_borrow_mut` (stale/busy
//     resource: dynamic borrow rules are errors, never panics);
//     library/core/src/ops/function.rs doc examples `call_with_one` (Fn),
//     `do_twice` (FnMut) and `consume_with_relish` (FnOnce) (closure/callback:
//     a callback invoked once, twice, and one that cannot be invoked again).
//   - serde-rs/json v1.0.151, commit de8500740cdcabffb9734f503e4889def823cf10,
//     "MIT OR Apache-2.0" (Cargo.toml `license`):
//     tests/test.rs `test_write_newtype_struct` (serde handle: a newtype
//     nested in an outer map crosses through to_value and back).
//
// Toolchain pin: rustc/cargo 1.93.1, serde 1.0.229, serde_json 1.0.151,
// base64 0.22.1.
const rustBridgeFence = `
use bashpp::{Callback, Handle};
use serde::{Deserialize, Serialize};
use serde_json::{json, Value};
use std::cell::RefCell;
use std::sync::atomic::{AtomicI64, Ordering};

static DROPS: AtomicI64 = AtomicI64::new(0);

pub struct Res { pub n: i64 }
impl Drop for Res {
    fn drop(&mut self) { DROPS.fetch_add(1, Ordering::SeqCst); }
}
pub struct Other;

// --- ownership/drop: rc.rs test_live / test_dead / try_unwrap ---
pub fn open(n: i64) -> Handle<Res> { Handle::new(Res { n }) }
pub fn other() -> Handle<Other> { Handle::new(Other) }
pub fn read(res: &Handle<Res>) -> Result<i64, String> { res.with(|r| r.n) }
pub fn bump(res: Handle<Res>) -> Result<i64, String> { res.with_mut(|r| { r.n += 1; r.n }) }
pub fn take(res: Handle<Res>) -> Result<i64, String> { res.take().map(|r| r.n) }
pub fn drops() -> i64 { DROPS.load(Ordering::SeqCst) }

// --- serde handle: test_write_newtype_struct ---
#[derive(Serialize, Deserialize)]
pub struct Newtype(pub Handle<Res>);
#[derive(Serialize, Deserialize)]
pub struct Outer { pub outer: Newtype }
pub fn wrap(res: Handle<Res>) -> Outer { Outer { outer: Newtype(res) } }
pub fn unwrap(outer: Outer) -> Result<i64, String> { outer.outer.0.with(|r| r.n) }

// --- stale/busy resource: cell.rs borrow rules, observed through a callback ---
pub fn while_shared(res: Handle<Res>, func: Callback) -> Result<Value, String> {
    res.with(|r| func.call(vec![json!(r.n)]))?
}
pub fn while_borrowed(res: Handle<Res>, func: Callback) -> Result<Value, String> {
    res.with_mut(|r| func.call(vec![json!(r.n)]))?
}

// --- closure/callback: ops/function.rs doc examples ---
pub fn call_with_one(func: Callback) -> Result<i64, String> { func.call_as(vec![json!(1)]) }
pub fn do_twice(func: Callback) -> Result<(), String> { func.call(vec![])?; func.call(vec![])?; Ok(()) }
pub fn consume_with_relish(func: Callback) -> Result<(), String> {
    println!("Consumed: {}", func.call_as::<String>(vec![])?);
    println!("Delicious!");
    Ok(())
}
thread_local! { static KEPT: RefCell<Option<Callback>> = const { RefCell::new(None) }; }
pub fn keep(func: Callback) { KEPT.with(|kept| *kept.borrow_mut() = Some(func)); }
pub fn call_kept() -> Result<Value, String> {
    let kept = KEPT.with(|kept| *kept.borrow()).ok_or("nothing kept")?;
    kept.call(vec![])
}
pub fn ping(func: Callback, n: i64) -> Result<i64, String> { func.call_as(vec![json!(n)]) }
pub fn after_callback(func: Callback) -> i64 { let _ = func.call(vec![]); panic!("after the callback") }
pub fn noisy(func: Callback) -> Result<(), String> { println!("before"); eprintln!("warn"); func.call(vec![])?; println!("after"); Ok(()) }
pub fn nap() { std::thread::sleep(std::time::Duration::from_secs(30)); }
`

func rustBridgeModule(t *testing.T) (*Module, Plan, Rust) {
	t.Helper()
	runtime := Rust{Command: rustToolchain(t)}
	plans, err := Prepare(context.Background(), []Block{{Language: "rust", Source: rustBridgeFence}}, map[string]Analyzer{"rust": runtime})
	if err != nil {
		t.Fatal(err)
	}
	module := Start(plans[0], runtime)
	t.Cleanup(func() { module.Close() })
	return module, plans[0], runtime
}

func rustBridgeHandle(t *testing.T, module *Module, name string, args ...any) *Handle {
	t.Helper()
	result, err := module.Call(context.Background(), name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	handle, ok := result.Value.(*Handle)
	if !ok {
		t.Fatalf("%s = %#v, want a handle", name, result.Value)
	}
	return handle
}

func rustBridgeInt(t *testing.T, module *Module, name string, args ...any) int64 {
	t.Helper()
	result, err := module.Call(context.Background(), name, args...)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	value, ok := result.Value.(int64)
	if !ok {
		t.Fatalf("%s = %#v, want an int", name, result.Value)
	}
	return value
}

func rustBridgeError(t *testing.T, module *Module, want string, name string, args ...any) error {
	t.Helper()
	_, err := module.Call(context.Background(), name, args...)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s error = %v, want %q", name, err, want)
	}
	return err
}

// TestRustHandlesOwnershipAndRelease is the B12 lifecycle: create/use, the
// handle inside an ordinary serde value, exact-once release, double release,
// a fence-side take, and a typed refusal.
func TestRustHandlesOwnershipAndRelease(t *testing.T) {
	module, plan, _ := rustBridgeModule(t)
	ctx := context.Background()
	want := map[string]Signature{
		"open":    {Params: []string{"int"}, Results: []string{"handle"}},
		"read":    {Params: []string{"handle"}, Results: []string{"int"}},
		"wrap":    {Params: []string{"handle"}, Results: []string{"object"}},
		"ping":    {Params: []string{"callback", "int"}, Results: []string{"int"}},
		"release": {Params: []string{"handle"}},
	}
	for _, export := range plan.Exports {
		if sig, ok := want[export.Name]; ok && !reflect.DeepEqual(sig, export.Signature) {
			t.Fatalf("export %s = %#v, want %#v", export.Name, export.Signature, sig)
		}
	}

	// test_live: a registered value is reachable through its handle, by
	// shared borrow and by mutable borrow; the token is Copy, so passing it
	// by value does not consume the value.
	res := rustBridgeHandle(t, module, "open", int64(3))
	if res.Type != "Res" || res.ID != 1 {
		t.Fatalf("handle = %#v", res)
	}
	if got := rustBridgeInt(t, module, "read", res); got != 3 {
		t.Fatalf("read = %d", got)
	}
	if got := rustBridgeInt(t, module, "bump", res); got != 4 {
		t.Fatalf("bump = %d", got)
	}
	// test_write_newtype_struct: the handle nested in a newtype inside an
	// outer struct crosses to the host as a value carrying the handle, and
	// the same value goes back.
	wrapped, err := module.Call(ctx, "wrap", res)
	if err != nil {
		t.Fatal(err)
	}
	outer, _ := wrapped.Value.(map[string]any)
	inner, _ := outer["outer"].(*Handle)
	if inner == nil || inner.ID != res.ID || inner.Type != "Res" {
		t.Fatalf("wrap = %#v", wrapped.Value)
	}
	if got := rustBridgeInt(t, module, "unwrap", wrapped.Value); got != 4 {
		t.Fatalf("unwrap = %d", got)
	}
	// Exact-once release: Drop runs on release, the second release and every
	// later use are refused as stale, and Drop does not run again (test_dead).
	if got := rustBridgeInt(t, module, "drops"); got != 0 {
		t.Fatalf("drops before release = %d", got)
	}
	if err := res.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if got := rustBridgeInt(t, module, "drops"); got != 1 {
		t.Fatalf("drops after release = %d", got)
	}
	if err := res.Release(ctx); err == nil || !strings.Contains(err.Error(), "stale Rust handle 1: released or never created") {
		t.Fatalf("double release error = %v", err)
	} else if detail, ok := ForeignErrorDetail(err); !ok || detail.Code != "RUST-EHANDLE" {
		t.Fatalf("double release detail = %#v", detail)
	}
	rustBridgeError(t, module, "Rust function read argument 1: stale Rust handle 1", "read", res)
	rustBridgeError(t, module, "stale Rust handle 1", "unwrap", wrapped.Value)
	if got := rustBridgeInt(t, module, "drops"); got != 1 {
		t.Fatalf("drops after refusals = %d", got)
	}
	// The synthetic release export is the same operation through Call.
	second := rustBridgeHandle(t, module, "open", int64(9))
	if _, err := module.Call(ctx, "release", second); err != nil {
		t.Fatal(err)
	}
	rustBridgeError(t, module, "stale Rust handle 2", "release", second)
	rustBridgeError(t, module, "expected a Rust handle, got string", "release", "nope")
	// try_unwrap Ok: the fence takes the value back, which releases the token.
	third := rustBridgeHandle(t, module, "open", int64(7))
	if got := rustBridgeInt(t, module, "take", third); got != 7 {
		t.Fatalf("take = %d", got)
	}
	if got := rustBridgeInt(t, module, "drops"); got != 3 {
		t.Fatalf("drops after take = %d", got)
	}
	rustBridgeError(t, module, "stale Rust handle 3", "read", third)
	// A handle of another type is refused with both types named.
	rustBridgeError(t, module, "Rust function read argument 1: Rust handle 4 is a Other, not a Res", "read", rustBridgeHandle(t, module, "other"))
	// A plain value where a handle is expected.
	rustBridgeError(t, module, "expected a Rust handle, got 5", "read", int64(5))
	// A handle from another module is foreign.
	foreign := Start(plan, Rust{Command: rustToolchain(t)})
	defer foreign.Close()
	if _, err := foreign.Call(ctx, "read", rustBridgeHandle(t, module, "open", int64(1))); err == nil || !strings.Contains(err.Error(), "stale or foreign Rust handle") {
		t.Fatalf("foreign handle error = %v", err)
	}
}

// TestRustHandlesStaleAfterWorkerDeath: a handle belongs to one worker
// generation. Cancellation kills the worker and Close ends it; either way the
// next call restarts it and the old handles are refused on the host.
func TestRustHandlesStaleAfterWorkerDeath(t *testing.T) {
	module, _, _ := rustBridgeModule(t)
	ctx := context.Background()
	res := rustBridgeHandle(t, module, "open", int64(1))
	timeout, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := module.Call(timeout, "nap"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nap error = %v", err)
	}
	if module.alive() {
		t.Fatal("cancelled worker was not terminated")
	}
	if err := res.Release(ctx); err == nil || !strings.Contains(err.Error(), "stale Rust handle: its worker is gone") {
		t.Fatalf("release after death error = %v", err)
	}
	if got := rustBridgeInt(t, module, "drops"); got != 0 {
		t.Fatalf("fresh worker drops = %d", got)
	}
	rustBridgeError(t, module, "stale Rust handle: its worker is gone", "read", res)
	again := rustBridgeHandle(t, module, "open", int64(2))
	if again.generation == res.generation || again.ID != 1 {
		t.Fatalf("restarted worker handle = %#v, old = %#v", again, res)
	}
	if err := module.Close(); err != nil {
		t.Fatal(err)
	}
	rustBridgeError(t, module, "stale Rust handle: its worker is gone", "read", again)
}

// TestRustCallbacksClosureExamples ports the Fn/FnMut/FnOnce doc examples:
// call_with_one(double) == 2, do_twice adds two twice, consume_with_relish
// prints and the callback cannot be invoked after its call returned.
func TestRustCallbacksClosureExamples(t *testing.T) {
	module, _, _ := rustBridgeModule(t)
	ctx := context.Background()
	double := Callback{Name: "double", Invoke: func(_ context.Context, args []any) (any, error) { return args[0].(int64) * 2, nil }}
	if got := rustBridgeInt(t, module, "call_with_one", double); got != 2 {
		t.Fatalf("call_with_one = %d", got)
	}
	// A Go function is a callback too, converted by FuncCallback.
	if got := rustBridgeInt(t, module, "call_with_one", func(x int) int { return x * 2 }); got != 2 {
		t.Fatalf("call_with_one(func) = %d", got)
	}
	x := 1
	addTwoToX := Callback{Name: "add_two_to_x", Invoke: func(context.Context, []any) (any, error) { x += 2; return nil, nil }}
	if _, err := module.Call(ctx, "do_twice", addTwoToX); err != nil || x != 5 {
		t.Fatalf("do_twice: x = %d, %v", x, err)
	}
	relish := Callback{Name: "consume_and_return_x", Invoke: func(context.Context, []any) (any, error) { return "x", nil }}
	if result, err := module.Call(ctx, "consume_with_relish", relish); err != nil || result.Stdout != "Consumed: x\nDelicious!\n" {
		t.Fatalf("consume_with_relish = %#v, %v", result, err)
	}
	// FnOnce: a callback kept past its call is expired, not invoked.
	invoked := 0
	kept := Callback{Name: "kept", Invoke: func(context.Context, []any) (any, error) { invoked++; return nil, nil }}
	if _, err := module.Call(ctx, "keep", kept); err != nil {
		t.Fatal(err)
	}
	rustBridgeError(t, module, "shell callback 5 expired: the call that passed it has completed", "call_kept")
	if invoked != 0 {
		t.Fatalf("expired callback was invoked %d times", invoked)
	}
	// Named callbacks resolve through the module's resolver; unknown names
	// and a missing resolver are refused before the worker is involved.
	rustBridgeError(t, module, `no shell callback resolver for "double"`, "call_with_one", "double")
	module.SetCallbacks(Callbacks{Resolve: func(name string) (Callback, bool) {
		if name == "double" {
			return double, true
		}
		return Callback{}, false
	}})
	if got := rustBridgeInt(t, module, "call_with_one", "double"); got != 2 {
		t.Fatalf("named call_with_one = %d", got)
	}
	rustBridgeError(t, module, `unknown shell callback "triple"`, "call_with_one", "triple")
}

// TestRustCallbacksFailures: a callback's error and a callback's panic are
// the worker function's error, a worker panic after a callback is a call
// error, and the worker survives all three.
func TestRustCallbacksFailures(t *testing.T) {
	module, _, _ := rustBridgeModule(t)
	ctx := context.Background()
	failing := Callback{Name: "failing", Invoke: func(context.Context, []any) (any, error) { return nil, errors.New("callback says no") }}
	err := rustBridgeError(t, module, "callback says no", "call_with_one", failing)
	if detail, ok := ForeignErrorDetail(err); !ok || detail.Code != "RUST-ECALL" {
		t.Fatalf("callback error detail = %#v", detail)
	}
	panicking := Callback{Name: "panicking", Invoke: func(context.Context, []any) (any, error) { panic("callback exploded") }}
	rustBridgeError(t, module, "shell callback panicking panicked: callback exploded", "call_with_one", panicking)
	noop := Callback{Name: "noop", Invoke: func(context.Context, []any) (any, error) { return nil, nil }}
	rustBridgeError(t, module, "Rust function panicked: after the callback", "after_callback", noop)
	wrong := Callback{Name: "wrong", Invoke: func(context.Context, []any) (any, error) { return "text", nil }}
	rustBridgeError(t, module, "shell callback result: invalid type: string", "call_with_one", wrong)
	if got := rustBridgeInt(t, module, "call_with_one", func(x int64) int64 { return x + 41 }); got != 42 {
		t.Fatalf("worker after failures = %d", got)
	}
	if _, err := module.Call(ctx, "ping", func(context.Context) {}, int64(1)); err == nil || !strings.Contains(err.Error(), "expects 1 arguments, got 1") == false && !strings.Contains(err.Error(), "cannot use int64 as context.Context") {
		t.Fatalf("mismatched Go callback error = %v", err)
	}
}

// TestRustCallbacksBoundedReentry: a callback may call the worker again on
// the same protocol stream, last-in-first-out, and the mutual recursion is
// bounded at MaxCallbackDepth callbacks. Cell.rs borrow rules are observed
// through such re-entry: a shared borrow admits another shared borrow and
// refuses a mutable one or a take; a mutable borrow refuses a shared one;
// both are released when the call returns.
func TestRustCallbacksBoundedReentry(t *testing.T) {
	module, _, _ := rustBridgeModule(t)
	ctx := context.Background()
	var depth, deepest int32
	var pinger Callback
	pinger = Callback{Name: "pinger", Invoke: func(ctx context.Context, args []any) (any, error) {
		n := args[0].(int64)
		if d := atomic.AddInt32(&depth, 1); d > atomic.LoadInt32(&deepest) {
			atomic.StoreInt32(&deepest, d)
		}
		defer atomic.AddInt32(&depth, -1)
		result, err := module.Call(ctx, "ping", pinger, n+1)
		if err != nil {
			return nil, err
		}
		return result.Value, nil
	}}
	err := rustBridgeError(t, module, "shell callback pinger re-entry exceeds the bound of 8", "ping", pinger, int64(0))
	if deepest != MaxCallbackDepth {
		t.Fatalf("deepest callback = %d, want %d", deepest, MaxCallbackDepth)
	}
	if detail, ok := ForeignErrorDetail(err); !ok || detail.Code != "RUST-ECALL" {
		t.Fatalf("bound error detail = %#v", detail)
	}
	// Bounded recursion that stops on its own returns through every level.
	var counter Callback
	counter = Callback{Name: "counter", Invoke: func(ctx context.Context, args []any) (any, error) {
		n := args[0].(int64)
		if n >= 5 {
			return n, nil
		}
		result, err := module.Call(ctx, "ping", counter, n+1)
		if err != nil {
			return nil, err
		}
		return result.Value, nil
	}}
	if got := rustBridgeInt(t, module, "ping", counter, int64(0)); got != 5 {
		t.Fatalf("counted ping = %d", got)
	}

	res := rustBridgeHandle(t, module, "open", int64(1))
	nested := func(name string, args ...any) Callback {
		return Callback{Name: name, Invoke: func(ctx context.Context, _ []any) (any, error) {
			result, err := module.Call(ctx, name, args...)
			if err != nil {
				return err.Error(), nil
			}
			return result.Value, nil
		}}
	}
	call := func(name string, args ...any) any {
		t.Helper()
		result, err := module.Call(ctx, name, args...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result.Value
	}
	// double_imm_borrow
	if got := call("while_shared", res, nested("read", res)); got != int64(1) {
		t.Fatalf("shared read during shared borrow = %#v", got)
	}
	// no_imm_then_borrow_mut
	if got, _ := call("while_shared", res, nested("bump", res)).(string); !strings.Contains(got, "Rust handle 1 is borrowed by a call still in progress") {
		t.Fatalf("mutable borrow during shared borrow = %#v", got)
	}
	// try_unwrap Err: another owner (the borrow) exists.
	if got, _ := call("while_shared", res, nested("take", res)).(string); !strings.Contains(got, "Rust handle 1 is borrowed by a call still in progress") {
		t.Fatalf("take during shared borrow = %#v", got)
	}
	// A host release during a shared borrow removes the token; the value is
	// dropped once, when the borrow ends.
	if got := call("while_shared", res, Callback{Name: "release", Invoke: func(ctx context.Context, _ []any) (any, error) {
		if err := res.Release(ctx); err != nil {
			return nil, err
		}
		result, err := module.Call(ctx, "drops")
		return result.Value, err
	}}); got != int64(0) {
		t.Fatalf("drops during shared borrow = %#v", got)
	}
	if got := rustBridgeInt(t, module, "drops"); got != 1 {
		t.Fatalf("drops after the borrow ended = %d", got)
	}
	rustBridgeError(t, module, "stale Rust handle 1", "read", res)
	// no_mut_then_imm_borrow, then imm/mut_release_borrow_mut.
	res = rustBridgeHandle(t, module, "open", int64(10))
	if got, _ := call("while_borrowed", res, nested("read", res)).(string); !strings.Contains(got, "Rust handle 2 is mutably borrowed by a call still in progress") {
		t.Fatalf("shared read during mutable borrow = %#v", got)
	}
	if got := rustBridgeInt(t, module, "bump", res); got != 11 {
		t.Fatalf("bump after borrows released = %d", got)
	}
	// The worker passes a handle to the callback and gets it back intact.
	echoedValues, _ := call("while_shared", res, Callback{Name: "echo", Invoke: func(_ context.Context, args []any) (any, error) { return []any{args[0], res}, nil }}).([]any)
	echoed, _ := echoedValues[1].(*Handle)
	if len(echoedValues) != 2 || echoedValues[0] != int64(11) || echoed == nil || echoed.ID != res.ID || echoed.generation != res.generation {
		t.Fatalf("handle through a callback = %#v", echoedValues)
	}
	if got := rustBridgeInt(t, module, "read", echoed); got != 11 {
		t.Fatalf("read through the echoed handle = %d", got)
	}
}

// TestRustCallbacksCancellationAndOutput: cancellation during a callback
// ends the call promptly and kills the worker; a nested call that did not
// carry the callback's context waits only as long as its own context allows
// instead of deadlocking; island output produced before a callback reaches
// the shell before the callback's own output.
func TestRustCallbacksCancellationAndOutput(t *testing.T) {
	module, _, _ := rustBridgeModule(t)
	ctx := context.Background()
	blocking := Callback{Name: "blocking", Invoke: func(ctx context.Context, _ []any) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	timeout, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := module.Call(timeout, "call_with_one", blocking); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled callback error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
	if module.alive() {
		t.Fatal("worker survived cancellation during a callback")
	}
	// Deadlock timeout: the nested call below does not use the callback's
	// context, so it cannot re-enter; it waits for the module and fails when
	// its own deadline passes, and the outer call completes with that error.
	misused := Callback{Name: "misused", Invoke: func(context.Context, []any) (any, error) {
		nested, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		_, err := module.Call(nested, "drops")
		return nil, err
	}}
	started = time.Now()
	rustBridgeError(t, module, context.DeadlineExceeded.Error(), "call_with_one", misused)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("misused nested call took %s", elapsed)
	}
	if got := rustBridgeInt(t, module, "call_with_one", func(x int64) int64 { return x }); got != 1 {
		t.Fatalf("worker after deadlock timeout = %d", got)
	}
	// Output ordering without a sink: island output accumulates into the
	// result; with a sink, the part before the callback is delivered first.
	var transcript strings.Builder
	speaker := Callback{Name: "speaker", Invoke: func(context.Context, []any) (any, error) { transcript.WriteString("callback\n"); return nil, nil }}
	result, err := module.Call(ctx, "noisy", speaker)
	if err != nil || result.Stdout != "before\nafter\n" || result.Stderr != "warn\n" || transcript.String() != "callback\n" {
		t.Fatalf("noisy without sink = %#v, %v, transcript %q", result, err, transcript.String())
	}
	transcript.Reset()
	module.SetCallbacks(Callbacks{Output: func(stdout, stderr string) { transcript.WriteString(stdout + stderr) }})
	result, err = module.Call(ctx, "noisy", speaker)
	transcript.WriteString(result.Stdout)
	if err != nil || transcript.String() != "before\nwarn\ncallback\nafter\n" || result.Stderr != "" {
		t.Fatalf("noisy with sink = %#v, %v, transcript %q", result, err, transcript.String())
	}
}

// Even a request rejected while encoding must expire callbacks registered
// earlier in that argument list, rather than retaining their closures forever.
func TestRustCallbacksEncodingFailureExpiresCallbacks(t *testing.T) {
	module, _, _ := rustBridgeModule(t)
	callback := Callback{Name: "unused", Invoke: func(context.Context, []any) (any, error) { return nil, nil }}
	_, err := module.Call(context.Background(), "ping", callback, &Handle{})
	if err == nil || !strings.Contains(err.Error(), "stale or foreign Rust handle") {
		t.Fatalf("encoding error = %v", err)
	}
	if len(module.callbackTable) != 0 || len(module.pendingCallbacks) != 0 {
		t.Fatalf("failed request retained callbacks: table=%d pending=%d", len(module.callbackTable), len(module.pendingCallbacks))
	}
}

func TestRustBridgeAnalysisRules(t *testing.T) {
	for source, want := range map[string]string{
		"use bashpp::Handle;\npub struct R;\npub fn open() -> Handle<R> { Handle::new(R) }\npub fn release(h: Handle<R>) {}\n": "Rust function release is reserved",
		"use bashpp::Callback;\npub fn give(f: Callback) -> Callback { f }\n":                                                  "a callback expires with the call that passed it",
	} {
		if _, err := analyzeRustExports(source); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: error = %v, want %q", source, err, want)
		}
	}
	exports, err := analyzeRustExports("pub struct R;\npub fn open() -> bashpp::Handle<R> { todo!() }\npub fn use_it(h: &bashpp::Handle<R>, f: &bashpp::Callback) {}\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(exports) != 3 || exports[2].Name != "release" || !exports[2].release || !reflect.DeepEqual(exports[1].Signature.Params, []string{"handle", "callback"}) {
		t.Fatalf("exports = %#v", exports)
	}
	source, err := rustWorkerSource("", exports)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(source, `__bpp::arity("release"`) {
		t.Fatalf("generated dispatcher has an arm for the synthetic release:\n%s", source)
	}
	// Without handles, release is an ordinary export with its own arm.
	exports, err = analyzeRustExports("pub fn release(x: i64) -> i64 { x }\n")
	if err != nil || len(exports) != 1 || exports[0].release {
		t.Fatalf("release without handles = %#v, %v", exports, err)
	}
	if source, err = rustWorkerSource("", exports); err != nil || !strings.Contains(source, `__bpp::arity("release", args, 1)`) {
		t.Fatalf("user release lacks its arm (%v):\n%s", err, source)
	}
}
