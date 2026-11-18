// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
)

func TestLookPathDirWindowsMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      expand.Environ
		file     string
		found    string
		want     string
		wantTry  []string
		wantExts []string
	}{
		{
			name:     "slash path skips PATH",
			env:      expand.ListEnviron("PATH=C:\\bin;D:/tools"),
			file:     `bin/rpc-server`,
			found:    `bin/rpc-server.exe`,
			want:     `bin/rpc-server.exe`,
			wantTry:  []string{`bin/rpc-server`},
			wantExts: []string{".com", ".exe", ".bat", ".cmd"},
		},
		{
			name:     "backslash path skips PATH",
			env:      expand.ListEnviron("PATH=C:\\bin;D:/tools"),
			file:     `bin\rpc-server`,
			found:    `bin\rpc-server.exe`,
			want:     `bin\rpc-server.exe`,
			wantTry:  []string{`bin\rpc-server`},
			wantExts: []string{".com", ".exe", ".bat", ".cmd"},
		},
		{
			name:     "drive path skips PATH",
			env:      expand.ListEnviron("PATH=C:\\bin;D:/tools"),
			file:     `C:\bin\rpc-server`,
			found:    `C:\bin\rpc-server.exe`,
			want:     `C:\bin\rpc-server.exe`,
			wantTry:  []string{`C:\bin\rpc-server`},
			wantExts: []string{".com", ".exe", ".bat", ".cmd"},
		},
		{
			name:     "PATH search appends requested PATHEXT",
			env:      expand.ListEnviron("PATH=C:\\bin;D:/tools", "PATHEXT=EXE;.BAT"),
			file:     `rpc-server`,
			found:    `D:/tools\rpc-server.exe`,
			want:     `D:/tools\rpc-server.exe`,
			wantTry:  []string{`C:\bin\rpc-server`, `D:/tools\rpc-server`},
			wantExts: []string{".exe", ".bat"},
		},
		{
			// Bash spells a hit through an empty (or ".") PATH element
			// "./name" on every platform — type5.sub sets PATH= and wants
			// `type -p e` to print "./e". filepath.Join would clean the
			// "./" away and leave a bare, slashless name.
			name:     "empty PATH entry searches dot",
			env:      expand.ListEnviron("PATH=;C:\\bin"),
			file:     `rpc-server`,
			found:    `./rpc-server.exe`,
			want:     `./rpc-server.exe`,
			wantTry:  []string{`./rpc-server`},
			wantExts: []string{".com", ".exe", ".bat", ".cmd"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotTry []string
			var gotExts []string
			find := func(_ string, file string, exts []string) (string, error) {
				gotTry = append(gotTry, file)
				gotExts = append([]string(nil), exts...)
				if file == trimWindowsExt(tt.found, exts) {
					return tt.found, nil
				}
				return "", os.ErrNotExist
			}
			got, err := lookPathDirMode(`C:\work`, tt.env, tt.file, find, true)
			if err != nil {
				t.Fatalf("lookPathDirMode returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("wrong result: want %q, got %q", tt.want, got)
			}
			if !reflect.DeepEqual(gotTry, tt.wantTry) {
				t.Fatalf("wrong lookup attempts:\nwant: %#v\ngot:  %#v", tt.wantTry, gotTry)
			}
			if !reflect.DeepEqual(gotExts, tt.wantExts) {
				t.Fatalf("wrong PATHEXT list:\nwant: %#v\ngot:  %#v", tt.wantExts, gotExts)
			}
		})
	}
}

func trimWindowsExt(path string, exts []string) string {
	for _, ext := range exts {
		if len(path) >= len(ext) && path[len(path)-len(ext):] == ext {
			return path[:len(path)-len(ext)]
		}
	}
	return path
}

func TestFindExecutableWithWindowsExtensions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	binDir := filepath.Join(dir, "bin")
	if err := os.Mkdir(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rpc-server.exe"), []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "rpc-server.exe"), []byte("echo ok\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		file string
		exts []string
		want string
	}{
		{
			name: "bare name keeps its spelling",
			file: "rpc-server",
			exts: []string{".exe"},
			want: "rpc-server",
		},
		{
			name: "relative slash path keeps its spelling",
			file: "bin/rpc-server",
			exts: []string{".com", ".exe"},
			want: "bin/rpc-server",
		},
		{
			name: "explicit exe returns as-is",
			file: "bin/rpc-server.exe",
			exts: []string{".exe"},
			want: "bin/rpc-server.exe",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := findExecutable(dir, tt.file, tt.exts)
			if err != nil {
				t.Fatalf("findExecutable returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("wrong result: want %q, got %q", tt.want, got)
			}
		})
	}
}

// The exec handler turns the display spelling back into the file to run.
func TestExecFileWithExt(t *testing.T) {
	t.Parallel()

	files := map[string]bool{`C:\t\bash.exe`: true, `C:\t\run.bat`: true, `C:\t\sh`: true, `C:\t\sh.exe`: true}
	exists := func(p string) bool { return files[p] }
	exts := []string{".com", ".exe", ".bat"}
	for _, tt := range []struct{ in, want string }{
		{`C:\t\bash`, `C:\t\bash.exe`},
		{`C:\t\bash.exe`, `C:\t\bash.exe`},
		{`C:\t\run`, `C:\t\run.bat`},
		{`C:\t\sh`, `C:\t\sh`}, // an extensionless file wins over its twin
		{`C:\t\none`, `C:\t\none`},
	} {
		if got := execFileWithExt(tt.in, exts, exists); got != tt.want {
			t.Errorf("execFileWithExt(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLookPathHasPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		file    string
		windows bool
		want    bool
	}{
		{"unix bare", "rpc-server", false, false},
		{"unix slash", "bin/rpc-server", false, true},
		{"unix backslash is name", `bin\rpc-server`, false, false},
		{"windows bare", "rpc-server", true, false},
		{"windows slash", "bin/rpc-server", true, true},
		{"windows backslash", `bin\rpc-server`, true, true},
		{"windows drive", `C:\bin\rpc-server`, true, true},
		{"windows bare drive", `C:`, true, true},
		{"windows lowercase drive", `d:tool`, true, true},
		{"windows colon inside a name", `<(:)`, true, false},
		{"windows trailing colon", `a:`, true, true},
		{"windows colon after two chars", `ab:c`, true, false},
		{"windows leading colon", `:x`, true, false},
		{"windows digit before colon", `1:x`, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lookPathHasPath(tt.file, tt.windows)
			if got != tt.want {
				t.Fatalf("wrong result: want %v, got %v", tt.want, got)
			}
		})
	}
}
