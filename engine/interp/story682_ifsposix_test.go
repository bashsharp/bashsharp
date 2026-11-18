// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
)

// Sprint 245, story 682: bash's ifs-posix.tests reported exactly half of its
// 6856 cases failing on Windows. The cause was not the in-process pipeline
// nor `read`: overlayEnviron.normalize used to fold EVERY variable name to
// upper case on Windows, so the fixture's expected-result variables `s`/`S`
// and `r`/`R` (split() assigns `i=$1 s=$2 r=$3` and resets `S` and `R` to
// empty in the same command) collided and were cleared, which made one of the two `for ifs in ': ' ' :'` iterations fail
// and suppressed the per-case diagnostics (`case $r in "$R") ;;`). Forcing
// the old folding on macOS reproduces `passed 3428 failed 3428` exactly.
//
// This test runs the fixture's split() shape — the `set --` half and the
// `echo | ( IFS=…; read x y; … )` half inside a command substitution — on
// every host, so the Windows CI leg proves the real pipeline + read path
// with the same variable names bash uses.
const ifsPosixSplitScript = `
failed=0
passed=0
split()
{
	i=$1 s=$2 r=$3 S='' R=''
	for ifs in ': ' ' :'
	do	IFS=$ifs
		g=` + "`" + `IFS=$ifs; x="$i"; set x $x; shift; case $# in 0) echo "[$#]" ;; 1) echo "[$#]($1)" ;; 2) echo "[$#]($1)($2)" ;; *) echo "[$#]($1)($2)($3)" ;; esac` + "`" + `
		case $g in
		"$s")	((passed+=1)) ;;
		*)	((failed+=1)); echo "set: IFS=\"$ifs\" i=\"$i\" expected \"$s\" got \"$g\"" ;;
		esac
		g=` + "`" + `export ifs; echo "$i" | ( IFS=$ifs; read x y; echo "($x)($y)" )` + "`" + `
		case $g in
		"$r")	((passed+=1)) ;;
		*)	((failed+=1)); echo "read: IFS=\"$ifs\" i=\"$i\" expected \"$r\" got \"$g\"" ;;
		esac
	done
}
split 'a' '[1](a)' '(a)()'
split 'a b' '[2](a)(b)' '(a)(b)'
split 'a:b' '[2](a)(b)' '(a)(b)'
split ':a' '[2]()(a)' '()(a)'
split 'a::b' '[3](a)()(b)' '(a)(:b)'
split ' a : b ' '[2](a)(b)' '(a)(b)'
echo "# passed $passed failed $failed"
`

func TestStory682IFSPosixSplitNamesStayDistinct(t *testing.T) {
	t.Parallel()
	file := parse(t, nil, ifsPosixSplitScript)
	var out bytes.Buffer
	// A process-like environment: only upper-case inherited names, so the
	// Windows fold-to-inherited rule has PATH to fold onto and nothing else.
	r, err := interp.New(
		interp.Env(expand.ListEnviron("PATH=/usr/bin:/bin", "HOME=/nonexistent")),
		interp.StdIO(nil, &out, &out),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), runnerRunTimeout)
	defer cancel()
	if err := r.Run(ctx, file); err != nil {
		t.Fatalf("run error: %v\noutput: %q", err, out.String())
	}
	got := strings.TrimSpace(out.String())
	const want = "# passed 24 failed 0"
	if got != want {
		t.Fatalf("ifs-posix split shape:\n got: %s\nwant: %s", got, want)
	}
}
