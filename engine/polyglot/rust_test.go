package polyglot

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"
)

// rustToolchain skips the test unless rustc and cargo are both installed: the
// worker is a cargo crate depending on serde, serde_json, and base64.
func rustToolchain(t *testing.T) string {
	t.Helper()
	rustc, err := exec.LookPath("rustc")
	if err != nil {
		t.Skip("rustc unavailable")
	}
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("cargo unavailable")
	}
	return rustc
}

func TestRustNativeArtifactCallsAndDiagnostics(t *testing.T) {
	runtime := Rust{Command: rustToolchain(t)}
	plans, err := Prepare(context.Background(), []Block{{Language: "rs", Source: `
pub fn add(a: i64, b: i64) -> i64 { println!("rust"); a + b }
pub fn greet(name: &str) -> String { format!("hello {name}") }
pub fn invert(value: bool) -> bool { !value }
pub fn blob(value: Vec<u8>) -> Vec<u8> { [value, vec![0xff]].concat() }
pub fn checked(value: i64) -> Result<i64, String> {
    if value < 0 { Err("negative".into()) } else { Ok(value) }
}
`}}, map[string]Analyzer{"rust": runtime})
	if err != nil {
		t.Fatal(err)
	}
	if got := plans[0]; got.Language != "rust" || len(got.Exports) != 5 || len(got.Artifact) == 0 {
		t.Fatalf("plan = %#v", got)
	}
	module := Start(plans[0], runtime)
	defer module.Close()
	result, err := module.Call(context.Background(), "add", int64(20), int64(22))
	if err != nil || result.Value != int64(42) || result.Stdout != "rust\n" {
		t.Fatalf("add = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "greet", "wor\x00ld")
	if err != nil || result.Value != "hello wor\x00ld" {
		t.Fatalf("greet = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "invert", true)
	if err != nil || result.Value != false {
		t.Fatalf("invert = %#v, %v", result, err)
	}
	result, err = module.Call(context.Background(), "blob", []byte{0, 1})
	if err != nil || string(result.Value.([]byte)) != string([]byte{0, 1, 0xff}) {
		t.Fatalf("blob = %#v, %v", result, err)
	}
	if _, err := module.Call(context.Background(), "checked", int64(-1)); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("checked error = %v", err)
	} else {
		detail, ok := ForeignErrorDetail(err)
		if !ok || detail.Code != "RUST-ECALL" || detail.Message != "negative" {
			t.Fatalf("structured Rust Result error = %#v, ok=%t", detail, ok)
		}
	}
}

// TestRustSerdeStructsPersistentWorker is the B3/B6 boundary matrix: ordinary
// `#[derive(Serialize, Deserialize)]` structs and Vec<Struct> cross the Bash#
// Object mapping with no Bash#-specific carrier type, and one worker serves the
// fence until it is cancelled or closed.
//
// Source-derived fixtures. Outer { inner: Vec<Inner> } and Inner { a: (), b:
// usize, c: Vec<String> } and the cases below (empty list, nested list of
// records, "invalid type: string ..., expected struct Outer", "missing field")
// are serde_json's own struct round-trip corpus: serde-rs/json v1.0.151,
// tests/test.rs `test_parse_struct` and `test_missing_nonoption_field`,
// licensed "MIT OR Apache-2.0" (Cargo.toml `license`). Non-finite floats
// yielding null follow `test_encode_nonfinite_float_yields_null` in the same
// file. Toolchain pin: rustc/cargo 1.93.1, serde 1.0.229, serde_json 1.0.151,
// base64 0.22.1. Bytes are the one boundary-specific shape: a Vec<u8> is an
// explicit $bytes value, never a JSON array of numbers.
func TestRustSerdeStructsPersistentWorker(t *testing.T) {
	runtime := Rust{Command: rustToolchain(t)}
	plans, err := Prepare(context.Background(), []Block{{Language: "rust", Source: `
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Inner {
    pub a: (),
    pub b: usize,
    pub c: Vec<String>,
}

#[derive(Clone, Debug, PartialEq, Serialize, Deserialize)]
pub struct Outer {
    pub inner: Vec<Inner>,
}

impl Outer {
    pub fn count(&self) -> usize { self.inner.len() }
}

pub fn make(b: usize) -> Outer {
    Outer { inner: vec![Inner { a: (), b, c: vec!["abc".to_owned(), "xyz".to_owned()] }] }
}
pub fn total(outer: Outer) -> usize { outer.inner.iter().map(|inner| inner.b).sum() }
pub fn bounce(outers: Vec<Outer>) -> Vec<Outer> { outers }
pub fn borrowed(outer: &Outer, names: &[String]) -> usize { outer.count() + names.len() }
pub fn maybe(value: Option<i64>) -> Option<i64> { value.map(|v| v * 2) }
pub fn pair() -> (i64, String) { (1, "one".to_owned()) }
pub fn nonfinite() -> f64 { f64::NAN }
pub fn checked(b: i64) -> Result<Outer, String> {
    if b < 0 { Err("negative".into()) } else { Ok(make(b as usize)) }
}
pub fn boom() -> i64 { panic!("kaboom") }
pub fn bump() -> i64 {
    static COUNT: std::sync::atomic::AtomicI64 = std::sync::atomic::AtomicI64::new(0);
    COUNT.fetch_add(1, std::sync::atomic::Ordering::SeqCst) + 1
}
pub fn noisy() { println!("out"); eprintln!("err"); }
pub fn nap() { std::thread::sleep(std::time::Duration::from_secs(30)); }
`}}, map[string]Analyzer{"rust": runtime})
	if err != nil {
		t.Fatal(err)
	}
	plan := plans[0]
	want := map[string]Signature{
		"make":      {Params: []string{"int"}, Results: []string{"object"}},
		"total":     {Params: []string{"object"}, Results: []string{"int"}},
		"bounce":    {Params: []string{"object"}, Results: []string{"object"}},
		"borrowed":  {Params: []string{"object", "object"}, Results: []string{"int"}},
		"maybe":     {Params: []string{"object"}, Results: []string{"object"}},
		"pair":      {Results: []string{"object"}},
		"nonfinite": {Results: []string{"float64"}},
		"checked":   {Params: []string{"int"}, Results: []string{"object"}},
		"noisy":     {},
	}
	for _, export := range plan.Exports {
		if export.Name == "count" {
			t.Fatal("the impl method count was exported")
		}
		if sig, ok := want[export.Name]; ok && !reflect.DeepEqual(sig, export.Signature) {
			t.Fatalf("export %s = %#v, want %#v", export.Name, export.Signature, sig)
		}
	}

	module := Start(plan, runtime)
	defer module.Close()
	ctx := context.Background()

	// Normal: a struct with a nested list-of-records field, serialized by serde.
	record := map[string]any{"inner": []any{map[string]any{"a": nil, "b": int64(2), "c": []any{"abc", "xyz"}}}}
	if got, err := module.Call(ctx, "make", int64(2)); err != nil || !reflect.DeepEqual(got.Value, record) {
		t.Fatalf("make = %#v, %v", got.Value, err)
	}
	// Normal: the same Object deserialized back into the struct.
	if got, err := module.Call(ctx, "total", record); err != nil || got.Value != int64(2) {
		t.Fatalf("total = %#v, %v", got.Value, err)
	}
	// Vec<Struct> in and out, including the empty list.
	list := []any{record, map[string]any{"inner": []any{}}}
	if got, err := module.Call(ctx, "bounce", list); err != nil || !reflect.DeepEqual(got.Value, list) {
		t.Fatalf("bounce = %#v, %v", got.Value, err)
	}
	if got, err := module.Call(ctx, "bounce", []any{}); err != nil || !reflect.DeepEqual(got.Value, []any{}) {
		t.Fatalf("bounce empty = %#v, %v", got.Value, err)
	}
	// Borrowed parameters deserialize to owned values and are lent to the call.
	if got, err := module.Call(ctx, "borrowed", record, []any{"x", "y"}); err != nil || got.Value != int64(3) {
		t.Fatalf("borrowed = %#v, %v", got.Value, err)
	}
	// Option, tuple, and non-finite float follow serde_json's own mapping.
	if got, err := module.Call(ctx, "maybe", nil); err != nil || got.Value != nil {
		t.Fatalf("maybe nil = %#v, %v", got.Value, err)
	}
	if got, err := module.Call(ctx, "maybe", int64(21)); err != nil || got.Value != int64(42) {
		t.Fatalf("maybe = %#v, %v", got.Value, err)
	}
	if got, err := module.Call(ctx, "pair"); err != nil || !reflect.DeepEqual(got.Value, []any{int64(1), "one"}) {
		t.Fatalf("pair = %#v, %v", got.Value, err)
	}
	if _, err := module.Call(ctx, "nonfinite"); err == nil || !strings.Contains(err.Error(), "float64 result annotation") {
		t.Fatalf("nonfinite error = %v", err)
	}
	// Failure: type mismatch and a missing field are serde's diagnostics.
	if _, err := module.Call(ctx, "total", "hello"); err == nil || !strings.Contains(err.Error(), `invalid type: string "hello", expected struct Outer`) {
		t.Fatalf("mismatch error = %v", err)
	}
	if _, err := module.Call(ctx, "total", map[string]any{}); err == nil || !strings.Contains(err.Error(), "missing field `inner`") {
		t.Fatalf("missing field error = %v", err)
	} else {
		detail, ok := ForeignErrorDetail(err)
		if !ok || detail.Code != "RUST-ECALL" || !strings.Contains(detail.Message, "missing field `inner`") {
			t.Fatalf("structured Rust serde error = %#v, ok=%t", detail, ok)
		}
	}
	// Failure: Result errors, panics, and arity are call errors, never worker deaths.
	if _, err := module.Call(ctx, "checked", int64(-1)); err == nil || !strings.Contains(err.Error(), "negative") {
		t.Fatalf("checked error = %v", err)
	}
	if got, err := module.Call(ctx, "checked", int64(5)); err != nil || got.Value.(map[string]any)["inner"].([]any)[0].(map[string]any)["b"] != int64(5) {
		t.Fatalf("checked ok = %#v, %v", got.Value, err)
	}
	if _, err := module.Call(ctx, "boom"); err == nil || !strings.Contains(err.Error(), "panicked: kaboom") {
		t.Fatalf("panic error = %v", err)
	}
	if _, err := module.Call(ctx, "bounce"); err == nil || !strings.Contains(err.Error(), "expects 1 arguments, got 0") {
		t.Fatalf("arity error = %v", err)
	}
	// Output isolation: island stdout/stderr come back with the call.
	if got, err := module.Call(ctx, "noisy"); err != nil || got.Value != nil || got.Stdout != "out\n" || got.Stderr != "err\n" {
		t.Fatalf("noisy = %#v, %v", got, err)
	}

	// Lifecycle: one worker keeps its state across calls, including after the
	// errors above; a second module from the same plan is a separate worker.
	for want := int64(1); want <= 3; want++ {
		if got, err := module.Call(ctx, "bump"); err != nil || got.Value != want {
			t.Fatalf("bump %d = %#v, %v", want, got.Value, err)
		}
	}
	fresh := Start(plan, runtime)
	defer fresh.Close()
	if got, err := fresh.Call(ctx, "bump"); err != nil || got.Value != int64(1) {
		t.Fatalf("fresh bump = %#v, %v", got.Value, err)
	}
	// Lifecycle: cancellation terminates the worker deterministically; the next
	// call restarts it from the immutable plan with fresh state.
	timeout, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if _, err := module.Call(timeout, "nap"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("nap error = %v", err)
	}
	if module.cmd != nil {
		t.Fatal("cancelled worker was not terminated")
	}
	if got, err := module.Call(ctx, "bump"); err != nil || got.Value != int64(1) {
		t.Fatalf("post-cancel bump = %#v, %v", got.Value, err)
	}
	// Lifecycle: Close terminates; a later call restarts.
	if err := module.Close(); err != nil {
		t.Fatal(err)
	}
	if got, err := module.Call(ctx, "bump"); err != nil || got.Value != int64(1) {
		t.Fatalf("post-close bump = %#v, %v", got.Value, err)
	}
}

func TestRustRejectsUnsupportedSurface(t *testing.T) {
	for source, want := range map[string]string{
		"pub fn borrowed() -> &str { \"x\" }\n":                          "borrowed result type &str",
		"pub fn slice() -> &[i64] { &[] }\n":                             "borrowed result type &[i64]",
		"pub fn mutate(value: &mut Vec<i64>) {}\n":                       "mutable borrows",
		"pub fn lifetime(value: &'static str) -> String { }\n":           "lifetimes",
		"pub fn opaque() -> impl std::fmt::Debug { 1 }\n":                "trait objects",
		"pub fn twice(value: i64) -> i64 { value }\npub fn twice() {}\n": "exported more than once",
	} {
		if _, err := analyzeRustExports(source); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%q: error = %v, want %q", source, err, want)
		}
	}
	// A serde-less struct is refused by rustc itself at preparation.
	if _, _, err := (Rust{Command: rustToolchain(t)}).AnalyzeArtifact(context.Background(), "pub struct Plain { pub x: i64 }\npub fn plain() -> Plain { Plain { x: 1 } }\n"); err == nil || !strings.Contains(err.Error(), "Serialize") {
		t.Fatalf("serde-less struct error = %v", err)
	}
}

// The generated dispatcher is the only per-fence code; its shape is pinned so
// the boundary stays one serde decode per argument and one encode per result.
func TestRustWorkerSourceShape(t *testing.T) {
	exports, err := analyzeRustExports("pub fn f(a: &str, b: Vec<u8>, c: &[Item], d: Item) -> Result<Vec<Item>, String> { todo!() }\npub fn g() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	source, err := rustWorkerSource("", exports)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`__bpp::arity("f", args, 4)?;`,
		`let __bpp_arg0: String = __bpp::arg("f", args, 0)?;`,
		`let __bpp_arg1: Vec<u8> = __bpp::bytes_arg("f", args, 1)?;`,
		`let __bpp_arg2: Vec<Item> = __bpp::arg("f", args, 2)?;`,
		`let __bpp_arg3: Item = __bpp::arg("f", args, 3)?;`,
		`match f(&__bpp_arg0, __bpp_arg1, &__bpp_arg2, __bpp_arg3) { Ok(value) => value, Err(error) => return Err(error.to_string()) };`,
		`__bpp::value("f", __bpp_value)`,
		"            g();\n            Ok(serde_json::Value::Null)",
		"fn main() {\n    __bpp::serve(__bpp_dispatch);\n}",
	} {
		if !strings.Contains(source, want) {
			t.Fatalf("generated source lacks %q:\n%s", want, source)
		}
	}
	if !strings.Contains(source, "#[cfg(windows)]") || !strings.Contains(source, "SetStdHandle") {
		t.Fatal("worker runtime lacks the Windows output redirection")
	}
}
