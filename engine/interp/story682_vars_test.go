// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// Sprint 245, story 682 (ifs-posix): the names bash's ifs-posix.tests
// relies on being distinct — `s`/`S`, `r`/`R`, `x`/`y`, `ifs`/`IFS` — stay
// distinct on every platform. The fold-everything rule this replaced
// collapsed s/S and r/R and failed half the fixture; folding onto an
// inherited name, which replaced it, made varenv23.sub's `$a` resolve to
// an exported `A` (Sprint 246). Shell variables are simply
// case-sensitive.
func TestStory682IFSPosixNamesWindowsMode(t *testing.T) {
	t.Parallel()
	base := expand.ListEnviron("PATH=C:\\Windows", "TEMP=C:\\Temp")
	o := &overlayEnviron{parent: base, values: map[string]namedVariable{}}
	for _, name := range []string{"s", "S", "r", "R", "x", "y", "i", "g", "ifs", "IFS", "ksh_read", "f1"} {
		if got := o.normalize(name); got != name {
			t.Errorf("normalize(%q) = %q; the fixture needs its spelling kept", name, got)
		}
	}
	if got := o.normalize("path"); got != "path" {
		t.Errorf("normalize(path) = %q; a script's own name is never folded", got)
	}
}
