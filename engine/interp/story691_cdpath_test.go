// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import "testing"

// Sprint 246, story 691 (`cd -` on Windows): builtins1.sub leaves
// CDPATH=".:/tmp" set and then runs `cd /; cd /tmp; cd -`, expecting `cd -`
// to print "/". The CDPATH search must be skipped for an absolute operand,
// but the guard used [path/filepath.IsAbs], which is false for "/" on
// Windows. `cd /` therefore matched the "." element of CDPATH and silently
// became `cd .`, so $OLDPWD never became "/" and `cd -` printed the tests
// directory instead.
func TestCdpathSkipMode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path    string
		windows bool
		want    bool
	}{
		// The regression: absolute in the shell's spelling on Windows.
		{"/", true, true},
		{"/tmp", true, true},
		{"/c/Users/x", true, true},
		// Native Windows spellings are absolute too.
		{`C:\x`, true, true},
		{`C:/x`, true, true},
		{`\x`, true, true},
		// Explicitly relative operands never search CDPATH.
		{".", true, true},
		{"..", true, true},
		{"./x", true, true},
		{"../x", true, true},
		{`.\x`, true, true},
		{`..\x`, true, true},
		// Plain names do search CDPATH — that is the whole point.
		{"x", true, false},
		{"x/y", true, false},
		{".hidden", true, false},
		{"..dots", true, false},
		{"", true, true},
		// Unix keeps exactly the behaviour it had.
		{"/", false, true},
		{"/tmp", false, true},
		{".", false, true},
		{"./x", false, true},
		{"../x", false, true},
		{"x", false, false},
		{".hidden", false, false},
		{`.\x`, false, false},
	}
	for _, tc := range tests {
		if got := cdpathSkipMode(tc.path, tc.windows); got != tc.want {
			t.Errorf("cdpathSkipMode(%q, windows=%v) = %v, want %v",
				tc.path, tc.windows, got, tc.want)
		}
	}
}
