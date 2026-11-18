// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import "testing"

func TestShellPathWindowsRootTranslation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dir         string
		path        string
		wantAbs     bool
		wantOS      string
		wantAbsPath string
	}{
		{
			name:        "slash root uses current drive",
			dir:         `C:\work\repo`,
			path:        `/Windows/System32`,
			wantAbs:     true,
			wantOS:      `C:\Windows\System32`,
			wantAbsPath: `C:\Windows\System32`,
		},
		{
			name:        "backslash root uses current drive",
			dir:         `D:\work`,
			path:        `\tmp\file`,
			wantAbs:     true,
			wantOS:      `D:\tmp\file`,
			wantAbsPath: `D:\tmp\file`,
		},
		{
			name:        "drive absolute remains unchanged",
			dir:         `C:\work`,
			path:        `D:\bin\tool.exe`,
			wantAbs:     true,
			wantOS:      `D:\bin\tool.exe`,
			wantAbsPath: `D:\bin\tool.exe`,
		},
		{
			// A backslash in a relative shell operand is a filename
			// character, not a separator (Unix convention). NTFS cannot
			// store it: it reaches Windows as '|', which is never found.
			name:        "relative joins with cwd",
			dir:         `C:\work`,
			path:        `bin\tool.exe`,
			wantAbs:     false,
			wantOS:      "bin|tool.exe",
			wantAbsPath: "C:\\work\\bin|tool.exe",
		},
		{
			name:        "missing drive defaults to c",
			dir:         `/work`,
			path:        `/bin/sh`,
			wantAbs:     true,
			wantOS:      `C:\bin\sh`,
			wantAbsPath: `C:\bin\sh`,
		},
		{
			name:        "msys drive /c maps to C:",
			dir:         `C:\work`,
			path:        `/c/Users/Lern`,
			wantAbs:     true,
			wantOS:      `C:\Users\Lern`,
			wantAbsPath: `C:\Users\Lern`,
		},
		{
			name:        "msys drive /d maps to D: regardless of cwd",
			dir:         `C:\work`,
			path:        `/d/foo/bar`,
			wantAbs:     true,
			wantOS:      `D:\foo\bar`,
			wantAbsPath: `D:\foo\bar`,
		},
		{
			name:        "msys bare /c is the drive root",
			dir:         `C:\work`,
			path:        `/c`,
			wantAbs:     true,
			wantOS:      `C:\`,
			wantAbsPath: `C:\`,
		},
		{
			name:        "msys backslash form maps to drive",
			dir:         `/c/Users/liqiang/.config/bashy`,
			path:        `\c\Users\liqiang\.config\bashy`,
			wantAbs:     true,
			wantOS:      `C:\Users\liqiang\.config\bashy`,
			wantAbsPath: `C:\Users\liqiang\.config\bashy`,
		},
		{
			name:        "single-letter dir under root is not a drive ref",
			dir:         `C:\work`,
			path:        `/bin`,
			wantAbs:     true,
			wantOS:      `C:\bin`,
			wantAbsPath: `C:\bin`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellPathAbsMode(tt.path, true); got != tt.wantAbs {
				t.Fatalf("wrong absolute result: want %v, got %v", tt.wantAbs, got)
			}
			if got := shellPathToOSMode(tt.dir, tt.path, true); got != tt.wantOS {
				t.Fatalf("wrong os path:\nwant: %q\ngot:  %q", tt.wantOS, got)
			}
			if got := shellPathJoinAbsMode(tt.dir, tt.path, true); got != tt.wantAbsPath {
				t.Fatalf("wrong absolute path:\nwant: %q\ngot:  %q", tt.wantAbsPath, got)
			}
		})
	}
}

func TestShellPathWindowsFromOSTranslation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "drive root", path: `C:\`, want: "/c"},
		{name: "drive path", path: `C:\Users\Lern`, want: "/c/Users/Lern"},
		{name: "lowercase drive", path: `d:\work\repo`, want: "/d/work/repo"},
		{name: "mixed slash drive", path: `E:/tmp/file`, want: "/e/tmp/file"},
		{name: "unc path", path: `\\server\share\dir`, want: "//server/share/dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellPathFromOSMode(tt.path, true); got != tt.want {
				t.Fatalf("wrong shell path:\nwant: %q\ngot:  %q", tt.want, got)
			}
		})
	}
}

func TestShellPathPosixTranslation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dir  string
		path string
		want string
	}{
		{name: "absolute", dir: "/work", path: "/bin/sh", want: "/bin/sh"},
		{name: "relative", dir: "/work", path: "bin/sh", want: "/work/bin/sh"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shellPathJoinAbsMode(tt.dir, tt.path, false); got != tt.want {
				t.Fatalf("wrong path:\nwant: %q\ngot:  %q", tt.want, got)
			}
		})
	}
}

func TestNativeExecEnvWindows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  []string
		want []string
	}{
		{
			// Sprint 245, story 682: values are the script's, not the
			// host's — no name is converted by default any more.
			name: "the host's temp variables convert; TMPDIR does not",
			env:  []string{"TEMP=/c/Users/x/AppData/Local/Temp"},
			want: []string{`TEMP=C:\Users\x\AppData\Local\Temp`},
		},
		{
			name: "other drive letters",
			env:  []string{"TMP=/d/w/a", "GOCACHE=/e/cache"},
			want: []string{`TMP=D:\w\a`, "GOCACHE=/e/cache"},
		},
		{
			name: "drive root",
			env:  []string{"HOME=/c", "USERPROFILE=/c/"},
			want: []string{"HOME=/c", `USERPROFILE=C:\`},
		},
		{
			name: "a bare posix path reaches the child intact",
			env:  []string{"HOME=/a/b/c"},
			want: []string{"HOME=/a/b/c"},
		},
		{
			name: "PATH elements",
			env:  []string{`PATH=/c/Go/bin;C:\Windows\System32;/d/tools/bin;.;`},
			want: []string{`PATH=C:\Go\bin;C:\Windows\System32;D:\tools\bin;.;`},
		},
		{
			name: "PATH matches case-insensitively",
			env:  []string{"Path=/c/Go/bin:/d/tools/bin"},
			want: []string{`Path=C:\Go\bin;D:\tools\bin`},
		},
		{
			name: "native values unchanged",
			env:  []string{`TEMP=C:\Users\x\AppData\Local\Temp`, `PATH=C:\Go\bin;C:\Windows`, `HOME=\c\Users\x`},
			want: []string{`TEMP=C:\Users\x\AppData\Local\Temp`, `PATH=C:\Go\bin;C:\Windows`, `HOME=\c\Users\x`},
		},
		{
			name: "not an msys drive path",
			// A relative or list-shaped value is not a single path: only an
			// absolute one converts, and TMPDIR stays the shell's.
			env:  []string{"TMPDIR=/tmp", "TEMP=tmp/x", "GOPATH=/cc/go", "TMP=/c/a:/c/b", "TEMP="},
			want: []string{"TMPDIR=/tmp", "TEMP=tmp/x", "GOPATH=/cc/go", "TMP=/c/a:/c/b", "TEMP="},
		},
		{
			name: "unlisted variables unchanged",
			env:  []string{"PWD=/c/work", "FOO=/c/bar", "_=/c/Go/bin/go.exe"},
			want: []string{"PWD=/c/work", "FOO=/c/bar", "_=/c/Go/bin/go.exe"},
		},
		{
			name: "malformed entries kept",
			env:  []string{"", "NOEQUALS", "PATH=/c/t"},
			want: []string{"", "NOEQUALS", `PATH=C:\t`},
		},
		{
			name: "empty",
			env:  []string{},
			want: []string{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := append([]string(nil), tc.env...)
			got := nativeExecEnvMode(in, true)
			if len(got) != len(tc.want) {
				t.Fatalf("nativeExecEnvMode(%q) = %q, want %q", tc.env, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("nativeExecEnvMode(%q)[%d] = %q, want %q", tc.env, i, got[i], tc.want[i])
				}
			}
			// The caller's slice is never rewritten in place: the shell keeps
			// handing scripts the MSYS spelling.
			for i := range in {
				if in[i] != tc.env[i] {
					t.Errorf("input[%d] mutated to %q", i, in[i])
				}
			}
			if got := nativeExecEnvMode(in, false); len(got) != len(in) {
				t.Fatalf("non-windows: %q, want input unchanged", got)
			} else {
				for i := range got {
					if got[i] != in[i] {
						t.Errorf("non-windows[%d] = %q, want %q", i, got[i], in[i])
					}
				}
			}
		})
	}
}
