Unchanged upstream originals for Sprint #118 Story #54 (`c3a60493cde9`).

Vendored verbatim from
`$HOME/projects/poc/dhnt/bashpp-tests/examples/{waitgroups,atomic-counters,mutexes}/`,
byte for byte, and pinned by SHA-256 in `sha256.json` against the upstream file
— not against the vendored copy — so a re-vendor that edited the source would
fail the digest rather than pass the test.

All three launch their goroutines with `sync.WaitGroup.Go`, which is the
surface `interp/gosource_waitgroup.go` answers.
