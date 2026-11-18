Sprint: #118; Story: #54; Story-ID: c3a60493cde9

The native slice transport supports synchronous byte-buffer Read/ReadAt methods
on dependency-owned strings.Reader, bytes.Reader, bytes.Buffer, bufio.Reader and
os.File handles, plus io.ReadFull/ReadAtLeast with those native readers. The
original program's expressions and method bodies remain interpreted.

A request captures the original slice header, refreshes its values after argument
evaluation, and supplies a full-capacity backing array with the original visible
length. The dependency returns that backing array even when Read reports an
error. Validated updates are copied into the captured original backing range,
preserving interpreted aliases and deferred-call header identity. The request
retains its own targets; no process-wide Runner or variable lookup is captured.

This is not generic shared-memory transport. Unreviewed mutation or retention
calls, original reader implementations, and original callbacks combined with
copied slices fail before dependency invocation. A small explicit list of
value-only dependency operations remains available. Cross-process pointer
address formatting and arbitrary retained/shared mutable references are outside
this slice. Native channels require their own identity/retention policy.

Validation performed with Go 1.27.0, GOMAXPROCS=2 and -p 2:

- Nine native/interpreter/compiled comparisons passed, including unchanged Tour
  reader.go (SHA-256 efe45163df17ea9c24da5ad0ba634265f680165183bb0bc98ed33b171c5164e0),
  overlapping interpreted aliases, restricted capacity, lazy zero capacity,
  partial-error writes, method values, deferred header capture, argument order,
  and native reader pointer state. Compiled artifacts run after source removal.
- The nine-program race run passed in 23.947s.
- Four explicit unsupported-boundary cases, cancellation/Reset, and existing
  native function/local codec/formatting/reference regressions passed under the
  race detector in 82.269s.
- A real stale native request replay after Reset passed its race regression in
  4.554s: the new session rejects the old reader handle and leaves old backing
  storage unchanged.

Durable raw evidence (including the original reader replay, artifact execution
with no tools on PATH, source digest, stdout/stderr and statuses) is at
~/.local/state/bashy/sprint118-evidence/native-slices-015/manifest.json.
The full sprint and unrelated interpreter coverage remain open.

Read-only regression repair:

- syscall.Exec reads the captured argv/env slices before replacing the dependency
  process; no original caller continuation retains those copies.
- The authenticated standard-library SHA-256 Digest.Write receiver consumes bytes
  without modifying or retaining them. Arbitrary methods named Write stay denied.
- Direct text/template.Template.Execute may receive primitive []string data only
  after the authenticated native template's parse tree passes a function-free
  inspection. Function identifiers, associated-template invocations and unknown
  node forms are refused. Existing original-callback/reference checks still run.
  This deliberately does not approve arbitrary configured template functions.

The unchanged Go by Example execing-processes, sha256-hashes and text-templates
sources pass native/interpreted/compiled comparisons with identical raw streams.
The process example runs all modes against the same unchanged assets directory
and retains its required system ls dependency. Generated Go is removed before
artifact execution. Raw evidence is at
~/.local/state/bashy/sprint118-evidence/readonly-regressions-017/manifest.json.
Race controls retain the old unsupported-reference tests and exercise read-only
SHA writes, primitive templates, refused associated-template invocation, and a
native dependency's configured template mutator that must never be invoked.
