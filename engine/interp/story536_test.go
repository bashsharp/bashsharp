// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"slices"
	"testing"
)

// Sprint 216, story 536: platform-neutral coverage for the Windows
// first-hour runtime pieces — BASHYENV env conversion and the per-drive
// cwd helpers. The Windows-only wiring is exercised by the build-tagged
// tests in windows_story536_test.go.

func TestNativeExecEnvBashyEnv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "p converts a single path",
			env:  []string{"BASHYENV=MYDIR/p", "MYDIR=/c/data", "OTHER=/c/x"},
			want: []string{"BASHYENV=MYDIR/p", `MYDIR=C:\data`, "OTHER=/c/x"},
		},
		{
			name: "l converts a colon list to a semicolon list",
			env:  []string{"BASHYENV=MYPATH/l", "MYPATH=/c/a:/d/b"},
			want: []string{"BASHYENV=MYPATH/l", `MYPATH=C:\a;D:\b`},
		},
		{
			name: "wsl mount form converts too",
			env:  []string{"BASHYENV=MYDIR/p", "MYDIR=/mnt/d/data"},
			want: []string{"BASHYENV=MYDIR/p", `MYDIR=D:\data`},
		},
		{
			name: "multiple entries",
			env:  []string{"BASHYENV=A/p:B/l", "A=/c/one", "B=/c/x:/c/y"},
			want: []string{"BASHYENV=A/p:B/l", `A=C:\one`, `B=C:\x;C:\y`},
		},
		{
			// Story 682 removed the built-in name list: TEMP is converted
			// only because BASHYENV asks for it.
			name: "a name is converted only when listed",
			env:  []string{"BASHYENV=TEMP/l", "TEMP=/c/a:/c/b", "TMPDIR=/c/a"},
			want: []string{"BASHYENV=TEMP/l", `TEMP=C:\a;C:\b`, "TMPDIR=/c/a"},
		},
		{
			name: "names match case-insensitively",
			env:  []string{"BASHYENV=mydir/p", "MyDir=/c/data"},
			want: []string{"BASHYENV=mydir/p", `MyDir=C:\data`},
		},
		{
			name: "entry without a supported flag is ignored",
			env:  []string{"BASHYENV=MYDIR/x:PLAIN", "MYDIR=/c/data", "PLAIN=/c/x"},
			want: []string{"BASHYENV=MYDIR/x:PLAIN", "MYDIR=/c/data", "PLAIN=/c/x"},
		},
		{
			name: "native values stay byte-identical",
			env:  []string{"BASHYENV=MYDIR/p:MYPATH/l", `MYDIR=C:\data`, `MYPATH=C:\a;C:\b`},
			want: []string{"BASHYENV=MYDIR/p:MYPATH/l", `MYDIR=C:\data`, `MYPATH=C:\a;C:\b`},
		},
		{
			name: "PATH is still converted alongside",
			env:  []string{"BASHYENV=MYDIR/p", "MYDIR=/c/data", "PATH=/c/t"},
			want: []string{"BASHYENV=MYDIR/p", `MYDIR=C:\data`, `PATH=C:\t`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := slices.Clone(tc.env)
			got := nativeExecEnvMode(in, true)
			if !slices.Equal(got, tc.want) {
				t.Errorf("nativeExecEnvMode(%q) = %q, want %q", tc.env, got, tc.want)
			}
			// The caller's slice is never rewritten in place.
			if !slices.Equal(in, tc.env) {
				t.Errorf("input mutated to %q", in)
			}
			if got := nativeExecEnvMode(in, false); !slices.Equal(got, in) {
				t.Errorf("non-windows = %q, want input unchanged", got)
			}
		})
	}
}

func TestParseBashyEnv(t *testing.T) {
	t.Parallel()

	if m := parseBashyEnv(""); m != nil {
		t.Errorf("parseBashyEnv(empty) = %v, want nil", m)
	}
	m := parseBashyEnv("A/p:B/l:C/pl:noflag:/p:D/x")
	want := map[string]byte{"A": 'p', "B": 'l', "C": 'l'}
	if len(m) != len(want) {
		t.Fatalf("parseBashyEnv = %v, want %v", m, want)
	}
	for k, v := range want {
		if m[k] != v {
			t.Errorf("parseBashyEnv[%q] = %q, want %q", k, m[k], v)
		}
	}
}

func TestDriveOperandTarget(t *testing.T) {
	t.Parallel()

	recorded := map[byte]string{'D': `D:\work\repo`}

	tests := []struct {
		operand string
		cwd     map[byte]string
		want    string
		wantOK  bool
	}{
		{"D:", recorded, `D:\work\repo`, true},
		{"d:", recorded, `D:\work\repo`, true},
		{"E:", recorded, `E:\`, true},
		{"C:", nil, `C:\`, true},
		{"D:x", recorded, "", false},
		{"D", recorded, "", false},
		{"::", recorded, "", false},
		{"1:", recorded, "", false},
		{"", recorded, "", false},
		{`D:\`, recorded, "", false},
	}
	for _, tt := range tests {
		got, ok := driveOperandTarget(tt.operand, tt.cwd)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("driveOperandTarget(%q) = (%q, %v), want (%q, %v)",
				tt.operand, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestRecordDriveCwd(t *testing.T) {
	t.Parallel()

	var m map[byte]string
	m = recordDriveCwd(m, `/tmp`) // no drive prefix: ignored
	if m != nil {
		t.Fatalf("recordDriveCwd(/tmp) allocated %v", m)
	}
	m = recordDriveCwd(m, `C:\work`)
	m = recordDriveCwd(m, `d:\data`)
	m = recordDriveCwd(m, `C:\other`) // last cd on a drive wins
	if got := m['C']; got != `C:\other` {
		t.Errorf("m['C'] = %q, want C:\\other", got)
	}
	if got := m['D']; got != `d:\data` {
		t.Errorf("m['D'] = %q, want d:\\data", got)
	}
}
