// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package syntax

// Brace-form if deliberately remains absent from RecognizeStartSite. Unlike
// the bounded prefix sites, a Go `if cond { body }` cannot be distinguished
// from a legal shell condition until the matching brace is followed by
// something other than `then`.
//
// Parser.bashppIf supplies the separate mechanism this site needs: only inside
// an already committed Bash++ function region it reads the complete candidate
// in a parserTransaction. A classic shell `then`, a non-final token after the
// brace, or any incomplete structural shape rolls back parser state,
// diagnostics, buffered bytes, and bytes read from the source. A complete
// brace form commits to BashPPIf; expression malformations are diagnosed only
// after that classification is final.
//
// Keeping this distinction explicit is important. StartGoIf identifies the
// typed AST's provenance, but the bounded recognizer must continue returning
// StartNone for `if`: expanding its lookahead contract would reintroduce the
// streaming and shell-compatibility failures covered by
// bashpp_braceif_probe_test.go and bashpp_if_test.go.
