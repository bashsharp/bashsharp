// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package interp

import (
	"io/fs"
	"runtime"
	"strings"

	"github.com/qiangli/bash/pathconv"
)

// The conversion core lives in the reusable github.com/qiangli/bash/pathconv package;
// these wrappers keep the interpreter's historical names so the many call
// sites (cd/PWD, open, stat, glob, exec) stay unchanged.

func shellPathAbs(path string) bool {
	return pathconv.IsAbs(path)
}

func shellPathAbsMode(path string, windows bool) bool {
	return pathconv.IsAbsMode(path, windows)
}

func shellPathJoinAbs(dir, path string) string {
	return pathconv.JoinAbs(dir, path)
}

func shellPathJoinAbsMode(dir, path string, windows bool) string {
	return pathconv.JoinAbsMode(dir, path, windows)
}

// ShellPathToOS converts a path in the shell's own spelling into the host's
// native form, resolved against dir when relative: on Windows the MSYS drive
// form (/c/…), the WSL form (/mnt/c/…), a drive-relative /foo and C:\… all
// become real drive paths; on every other host the path is returned
// unchanged. Embedders that open a script operand themselves (bashy's
// argv[1], `bashy -c` callers) use it so `bashy "$HOME/x.sh"` works on
// Windows, where $HOME is /c/Users/…. See [pathconv.ToOS].
func ShellPathToOS(dir, path string) string { return shellPathToOS(dir, path) }

func shellPathToOS(dir, path string) string {
	return pathconv.ToOS(dir, path)
}

func shellPathToOSMode(dir, path string, windows bool) string {
	return pathconv.ToOSMode(dir, path, windows)
}

func shellPathFromOS(path string) string {
	return pathconv.FromOS(path)
}

func shellPathFromOSMode(path string, windows bool) string {
	return pathconv.FromOSMode(path, windows)
}

// Why no default list of path-valued variable names: earlier sprints
// converted TEMP, HOME, GOPATH and friends to the native spelling for every
// child, because a native toolchain (go.exe, rustc) cannot make sense of
// /c/Users/… and dies with "creating work dir … D:\c\Users\…". That cure is
// worse than the disease now that the userland a script actually calls is
// bashy's own: its applets go through pathconv and understand the shell's
// spelling, so converting the value only corrupts it — `HOME=/a/b/c
// /bin/echo $HOME` (varenv.tests) printed the mounted D:\w\root\a\b\c
// instead of the /a/b/c bash prints, and any variable holding a path that is
// not a filesystem path at all was rewritten behind the script's back.
//
// So the default is to hand a child its values byte-identical. Two
// exceptions remain, both deliberate:
//
//   - PATH, because the exec lookup itself walks those entries and needs
//     native directories (see [nativeExecPathListMounts]).
//   - TMP and TEMP, which are the host's own variables: os.TempDir reads
//     them, and the /tmp mount is built from that, so a child shell handed
//     the shell's spelling loses its temp directory entirely. TMPDIR — the
//     POSIX name, which the shell owns — is passed through untouched.
//   - the names listed in BASHYENV (VAR/p:VAR2/l), the explicit per-variable
//     opt-in a script uses when it does invoke a native tool that needs the
//     Windows spelling. See [parseBashyEnv].

// parseBashyEnv parses the BASHYENV variable, a WSLENV-style opt-in list of
// variables to path-convert at the child-process boundary:
//
//	BASHYENV=VAR/p:VAR2/l
//
// /p converts the value as a single path, /l as a path list (a :-separated
// shell-form list becomes ;-separated native). Names are matched
// case-insensitively; an entry without a supported flag is ignored.
func parseBashyEnv(spec string) map[string]byte {
	var m map[string]byte
	for entry := range strings.SplitSeq(spec, ":") {
		name, flags, ok := strings.Cut(entry, "/")
		if !ok || name == "" {
			continue
		}
		var flag byte
		switch {
		case strings.ContainsRune(flags, 'l'):
			flag = 'l'
		case strings.ContainsRune(flags, 'p'):
			flag = 'p'
		default:
			continue
		}
		if m == nil {
			m = make(map[string]byte)
		}
		m[strings.ToUpper(name)] = flag
	}
	return m
}

// nativeExecEnv prepares a child's environment on Windows. Only PATH is
// converted by default — each element becomes a native directory, because
// the exec lookup walks them — plus whatever variables BASHYENV
// (VAR/p:VAR2/l) opts in, per their flag. Every other value is handed to the
// child byte-identical, in the shell's own spelling, so `HOME=/a/b/c cmd`
// gives cmd /a/b/c; see the note above [parseBashyEnv] for why there is no
// built-in list of path-valued names. The shell's own variables are
// untouched either way. On every other host env is returned as is.
// NativeExecEnv prepares a shell environment for a native child executable.
// ExecHandler middleware that launches a process directly should use this
// conversion after applying its environment overrides. On Windows it preserves
// shell PATH spelling for this executable and converts native path variables
// for other children, using the same policy as DefaultExecHandler.
// The input slice is not modified.
func NativeExecEnv(env []string, executable string) []string {
	return nativeExecEnvForChild(env, executable)
}

func nativeExecEnv(env []string) []string {
	return nativeExecEnvMode(env, runtime.GOOS == "windows")
}

func nativeExecEnvMode(env []string, windows bool) []string {
	return nativeExecEnvMountsMode(pathconv.CurrentMounts(), env, windows)
}

// nativeExecEnvForChildMountsMode keeps the shell's PATH spelling when the
// child is this Bashy executable. The child performs its own PATH lookup and
// understands POSIX mount paths, while a native Windows program still needs
// the converted semicolon-separated PATH. In particular, `command -p sh` is
// allowed to use the standard utility search path without changing the PATH
// exported to the child shell.
func nativeExecEnvForChildMountsMode(m *pathconv.Mounts, env []string, windows, selfChild bool) []string {
	native := nativeExecEnvMountsMode(m, env, windows)
	if !windows || !selfChild {
		return native
	}
	for i, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok || !strings.EqualFold(name, "PATH") || i >= len(native) || native[i] == kv {
			continue
		}
		// nativeExecEnvMountsMode may have returned the input slice when no
		// conversion was required; never change the runner's own environment.
		native = append([]string(nil), native...)
		native[i] = kv
	}
	return native
}

// nativeExecEnvMountsMode is [nativeExecEnvMode] with an explicit mount
// table, so the virtual root (/bin, /tmp) can be exercised on any host.
// windowsHostTempEnv reports the host's own directory variables: TMP and
// TEMP, which os.TempDir consults, and USERPROFILE, which os.UserHomeDir
// and Windows programs such as podman read as the home directory.
func windowsHostTempEnv(name string) bool {
	return strings.EqualFold(name, "TMP") || strings.EqualFold(name, "TEMP") ||
		strings.EqualFold(name, "USERPROFILE")
}

func nativeExecEnvMountsMode(m *pathconv.Mounts, env []string, windows bool) []string {
	if !windows {
		return env
	}
	var bashyEnv map[string]byte
	for _, kv := range env {
		if name, value, ok := strings.Cut(kv, "="); ok && strings.EqualFold(name, "BASHYENV") {
			bashyEnv = parseBashyEnv(value)
			break
		}
	}
	var out []string
	for i, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || value == "" {
			continue
		}
		var conv string
		if flag, listed := bashyEnv[strings.ToUpper(name)]; listed {
			if flag == 'l' {
				conv = pathconv.NativePathList(value)
			} else {
				conv = pathconv.NativePath(value)
			}
		} else if strings.EqualFold(name, "PATH") {
			conv = nativeExecPathListMounts(m, value)
		} else if windowsHostTempEnv(name) {
			// TMP and TEMP are Windows' own variables, not shell text: every
			// native program reads them to find the temp directory, this
			// shell included (os.TempDir backs the /tmp mount). Handing a
			// child the shell's spelling makes ITS /tmp resolve to a
			// drive-relative \tmp that does not exist, which took 15
			// fixtures with it. The POSIX spelling belongs in TMPDIR, which
			// the shell owns and passes through untouched. USERPROFILE is
			// the same kind of variable: podman panicked on /c/Users/x.
			conv = pathconv.NativePathMounts(m, value)
		} else {
			continue
		}
		if conv == value {
			continue
		}
		if out == nil {
			out = append([]string(nil), env...)
		}
		out[i] = name + "=" + conv
	}
	res := env
	if out != nil {
		res = out
	}
	// Exported names that collide under Windows' case folding cannot all
	// survive os/exec's dedupe, so the full case-sensitive set rides along
	// in [bashyCasedEnv] for a child shell to restore.
	if marker := casedEnvMarker(res); marker != "" {
		if out == nil {
			res = append([]string(nil), env...)
		}
		res = setExecEnvValue(res, bashyCasedEnv, marker)
	}
	return res
}

// nativeExecPathListMounts converts each element of the shell's PATH to
// native form for a child: the list is split with [pathconv.SplitPathList]
// (';' when present, else the POSIX ':' with native drive paths re-joined),
// MSYS-form and mounted elements (/c/x, /bin, /usr/bin) become native
// directories, and the result is ';'-separated. Other elements stay as
// they are.
func nativeExecPathListMounts(m *pathconv.Mounts, value string) string {
	if !strings.Contains(value, "/") {
		return value
	}
	return pathconv.NativePathListMounts(m, value)
}

// decodeDirEntries hands globbing the POSIX spelling of directory entries
// on Windows: a name stored as a\uf03ab (the Cygwin/MSYS encoding of a:b
// that [pathconv.ToOS] writes) is decoded so the shell word, its sort
// order and pattern matching see the character the script used. Entries
// without an encoded rune are returned as they are.
func decodeDirEntries(entries []fs.DirEntry, windows bool) []fs.DirEntry {
	if !windows {
		return entries
	}
	for i, e := range entries {
		name := e.Name()
		if decoded := pathconv.DecodeSpecialMode(name, true); decoded != name {
			entries[i] = decodedDirEntry{DirEntry: e, name: decoded}
		}
	}
	return entries
}

// decodedDirEntry is an fs.DirEntry whose Name is the decoded spelling.
type decodedDirEntry struct {
	fs.DirEntry
	name string
}

func (d decodedDirEntry) Name() string { return d.name }
