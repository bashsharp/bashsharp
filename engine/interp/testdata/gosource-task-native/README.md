Sprint: #243; Story: #674; Story-ID: 63073886bfce

Unchanged originals from the Go distribution's `test/` directory (release and
per-file SHA256 in `provenance.json`; `LICENSE` is the Go BSD license). Both
programs launch a callee that has no original function body:

- `goprint.go.txt` — `go println(…)` of eleven typed operands; `goprint.out.txt`
  is the toolchain's expected combined output.
- `issue25897b.go.txt` — `go f(c)` a hundred times, where `f` is a reflected
  method value of a local type obtained through `reflect.Value.MethodByName`.

They are measured by `gosource_task_native_test.go` against the native oracle.
