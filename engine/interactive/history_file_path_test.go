// Copyright (c) 2026, the bashy authors
// See LICENSE for licensing information

//go:build !plan9 && !js

package interactive

import (
	"testing"

	"github.com/qiangli/bash/pathconv"
)

// The history file reaches the line editor in the shell's spelling
// ($HISTFILE, `$TMPDIR/newhistory-$$` in history4.sub with TMPDIR=/tmp on
// Windows) and is opened with plain os calls, so it is converted the way the
// interpreter converts its own opens: /tmp is the host temp directory, /c/…
// a drive path; native and relative spellings pass through, and no host but
// Windows converts at all.
func TestHistoryFilePathShellSpelling(t *testing.T) {
	oldTemp := pathconv.TempDir
	pathconv.TempDir = func() string { return `C:\Users\r\Temp` }
	defer func() { pathconv.TempDir = oldTemp }()

	const dir = `C:\work`
	cases := []struct {
		path, want string
		windows    bool
	}{
		{"/tmp/newhistory-42", `C:\Users\r\Temp\newhistory-42`, true},
		{"/c/Users/r/.bash_history", `C:\Users\r\.bash_history`, true},
		{`C:\Users\r\.bashy_history`, `C:\Users\r\.bashy_history`, true},
		{".hist", ".hist", true},
		{"", "", true},
		{"/tmp/newhistory-42", "/tmp/newhistory-42", false},
		{"/home/r/.bash_history", "/home/r/.bash_history", false},
	}
	for _, c := range cases {
		if got := historyFilePath(dir, c.path, c.windows); got != c.want {
			t.Errorf("historyFilePath(%q, windows=%v) = %q, want %q", c.path, c.windows, got, c.want)
		}
	}
}
