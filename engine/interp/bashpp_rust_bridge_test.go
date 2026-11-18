//go:build full

package interp_test

import (
	"strings"
	"testing"
)

// The B11/B12 fence shared by the interpreter tests here and the lowered
// parity tests in lower/polyglot_test.go. Fixture shapes port rust-lang/rust
// 59807616e library/core/src/ops/function.rs doc examples (call_with_one,
// do_twice, consume_with_relish), library/alloctests/tests/rc.rs test_live /
// test_dead and library/coretests/tests/cell.rs borrow rules ("MIT OR
// Apache-2.0"); see polyglot/rust_bridge_test.go for the full provenance.
const rustBridgeFence = `~~~rust as rs
use bashpp::{Callback, Handle};
use serde_json::json;
use std::sync::atomic::{AtomicI64, Ordering};

static DROPS: AtomicI64 = AtomicI64::new(0);
pub struct Res { pub n: i64 }
impl Drop for Res { fn drop(&mut self) { DROPS.fetch_add(1, Ordering::SeqCst); } }

pub fn open(n: i64) -> Handle<Res> { Handle::new(Res { n }) }
pub fn read(res: &Handle<Res>) -> Result<i64, String> { res.with(|r| r.n) }
pub fn bump(res: Handle<Res>) -> Result<i64, String> { res.with_mut(|r| { r.n += 1; r.n }) }
pub fn drops() -> i64 { DROPS.load(Ordering::SeqCst) }
pub fn call_with_one(func: Callback) -> Result<i64, String> { func.call_as(vec![json!(1)]) }
pub fn do_twice(func: Callback) -> Result<(), String> { func.call(vec![])?; func.call(vec![])?; Ok(()) }
pub fn consume_with_relish(func: Callback) -> Result<(), String> { println!("Consumed: {}", func.call_as::<String>(vec![])?); println!("Delicious!"); Ok(()) }
pub fn noisy(func: Callback) -> Result<(), String> { println!("before"); eprintln!("warn"); func.call(vec![])?; println!("after"); Ok(()) }
pub fn ping(func: Callback, n: i64) -> Result<i64, String> { func.call_as(vec![json!(n)]) }
pub fn visit(func: Callback, res: Handle<Res>) -> Result<i64, String> { func.call_as(vec![json!(res)]) }
pub fn while_shared(res: Handle<Res>, func: Callback) -> Result<i64, String> { res.with(|_| func.call_as::<i64>(vec![json!(res)]))? }
pub fn nap() { std::thread::sleep(std::time::Duration::from_secs(30)); }
~~~
`

const rustBridgeScript = rustBridgeFence + `
func double(x int) int { return x * 2 }
func speak() { echo "callback" }
func relish() string { return "x" }
func peek(h any) int {
	v := rs.read(h)
	return v
}
func pong(n int) int {
	if n >= 3 { return n }
	m := n + 1
	next := rs.ping(pong, m)
	return next
}
func tryBump(h any) int {
	v, err := rs.bump(h)
	echo "nested: $err"
	return v
}

h := rs.open(3)
echo "h=$h"
a := rs.read(h)
b := rs.bump(h)
echo "a=$a b=$b"
seen := rs.visit(peek, h)
echo "seen=$seen"
busy := rs.while_shared(h, tryBump)
echo "busy=$busy"
after := rs.bump(h)
echo "after=$after"
d0 := rs.drops()
rs.release(h)
d1 := rs.drops()
echo "drops=$d0,$d1"
err := rs.release(h)
echo "twice=$err"
again, readErr := rs.read(h)
echo "again=$again readErr=$readErr"
d2 := rs.drops()
echo "d2=$d2"
x := rs.call_with_one(double)
echo "x=$x"
n := 1
addTwo := func() { n = n + 2 }
rs.do_twice(addTwo)
echo "n=$n"
rs.consume_with_relish(relish)
rs.noisy(speak)
p := rs.ping(pong, 0)
echo "p=$p"
`

const (
	rustBridgeStdout = "h={\"id\":1,\"type\":\"Res\"}\na=3 b=4\nseen=4\nnested: RUST-ECALL: Rust handle 1 is borrowed by a call still in progress\nbusy=0\nafter=5\ndrops=0,1\ntwice=RUST-EHANDLE: stale Rust handle 1: released or never created\nagain=0 readErr=RUST-ECALL: Rust function read argument 1: stale Rust handle 1: released or never created\nd2=1\nx=2\nn=5\nConsumed: x\nDelicious!\nbefore\ncallback\nafter\np=3\n"
	rustBridgeStderr = "warn\n"
)

func TestBashPPRustHandlesAndCallbacks(t *testing.T) {
	requireRustToolchain(t)
	out, diagnostic, err := runPolyglot(t, rustBridgeScript)
	if err != nil || out != rustBridgeStdout || diagnostic != rustBridgeStderr {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
}

// Failures stay diagnostics with a failing status and the worker survives:
// a handle argument that is not a handle, a callback that fails, a callback
// that panics, an unknown callback name, and the re-entry bound.
func TestBashPPRustBridgeFailures(t *testing.T) {
	requireRustToolchain(t)
	out, diagnostic, err := runPolyglot(t, rustBridgeFence+`
func double(x int) int { return x * 2 }
func failing2(x int) int { exit 3 }
func boom(x int) int { panic("callback exploded") }
func forever(n int) int {
	m := n + 1
	next := rs.ping(forever, m)
	return next
}
v := rs.read(text)
echo "status=$?"
w := rs.call_with_one(failing2)
echo "status=$?"
y := rs.call_with_one(boom)
echo "status=$?"
z := rs.call_with_one(missing)
echo "status=$?"
deep := rs.ping(forever, 0)
echo "status=$?"
ok := rs.call_with_one(double)
echo "ok=$ok"
`)
	// A failed short declaration reports the assignment mismatch (status 2)
	// after the foreign call's own failure, as every foreign failure does;
	// the bounded recursion unwinds through the callback frames instead.
	if err != nil || out != "status=2\nstatus=2\nstatus=2\nstatus=2\nstatus=1\nok=2\n" {
		t.Fatalf("out=%q diagnostic=%q err=%v", out, diagnostic, err)
	}
	for _, want := range []string{
		"bash++: rs.read argument 1: want a Rust handle, got \"text\"",
		"shell callback failing2 failed (status 3)",
		"panic: callback exploded\nbash++: foreign call rs.call_with_one failed: RUST-ECALL: bash++: shell callback boom failed (status 2)",
		`unknown shell callback "missing"`,
		"shell callback forever re-entry exceeds the bound of 8",
	} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("diagnostic %q lacks %q", diagnostic, want)
		}
	}
}
