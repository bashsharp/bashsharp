# Bash++ fenced Rust implementation

Status: implemented September 2026 in `sh`, exposed through `bashy`, and
covered by the umbrella polyglot gate.

## Decision

Bash++ accepts `~~~rust` and `~~~rs` source fences. A fence is a Rust
declaration unit: its supported top-level `pub fn` functions become Bash++
callables, either directly or through `as alias`.

The implementation uses `rustc` to build a small native call worker. In the
interpreter, compilation happens while the source unit is prepared. In the Go
lowerer, the worker bytes are carried by the polyglot plan and embedded into
the generated program; the deployed program therefore does not require a Rust
toolchain. This preserves the shell core's no-cgo boundary and gives the
interpreter and lowered program the same bridge implementation.

The first bridge surface is intentionally explicit:

- parameters: Rust booleans, integer and float primitives, `String`, `&str`,
  and `Vec<u8>`;
- results: the same scalar/owned types, `Vec<u8>`, and `()`;
- fallible results: `Result<T, E>` with an error that implements `Display`;
- unsupported signatures fail during source preparation, and borrowed `&str`
  results direct authors to return `String`.

Calls preserve function stdout and stderr. Values use a framed, hex-safe
protocol, so arbitrary strings and bytes cannot collide with the result frame.
Panics and `Result::Err` values become Bash++ call failures. Named arguments
are rejected because Rust has no named-call semantics.

## Environment contract

Discovery starts from the Bash++ source location, recognizes `Cargo.toml`,
`Cargo.lock`, `rust-toolchain.toml`, and `rust-toolchain`, and records them in
the reproducibility fingerprint. `BASHPP_RUSTC` overrides the compiler;
otherwise the selected overlay runtime or `rustc` on `PATH` is used. Cargo is
identified as the project manager but is not invoked, and no dependencies are
installed implicitly.

Rust fences execute native user code with the same authority as the Bash++
task. They are not a sandbox. Hosted execution that accepts untrusted source
must add an outer process/container sandbox with resource and network policy.

## Why not an interpreter dependency

- Evcxr is a compiler-backed REPL and Jupyter runtime. Its session model is
  useful prior art, but adopting it would add a second protocol/runtime and
  would not remove the Rust compiler requirement.
- Rust Playground demonstrates the right trust boundary for hosted arbitrary
  Rust: native compilation inside a container with external resource controls.
- Miri is a nightly undefined-behavior detector for a constrained Rust
  execution environment, not a production sandbox or general interpreter.
- Cargo single-file/script support remains unsuitable as the stable execution
  contract. Calling `rustc` directly keeps the initial fence deterministic and
  dependency-free.

## Verification

Tests cover both fence spellings, parser direct-call registration, direct and
qualified calls, scalar/string/byte conversion, stdout, `Result` errors,
environment discovery, and interpreted/lowered-native parity. The
`bashpp-tests/tools/polyglot-gate.sh` product gate adds an installed-CLI Rust
case and mode-isolation checks.

## Follow-on surface

Crate dependencies, generic collections beyond `Vec<u8>`, structs/enums,
opaque handles, async functions, and optional Miri diagnostics are additive
features. They should not widen the default bridge implicitly: each needs a
stable serialization contract and parity tests first.
