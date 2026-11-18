package cli

import (
	"bufio"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// GNU Bash 5.3 startup (variables.c, initialize_shell_variables) binds a
// SHELL variable when — and only when — none was imported from the
// environment: the value is the current user's login shell
// (getpwuid(getuid())->pw_shell), falling back to "/bin/sh" when the passwd
// lookup fails or the shell field is empty. The synthesized variable is NOT
// exported, so a child's environment still omits SHELL. An imported SHELL —
// including an explicitly empty "SHELL=" — is preserved verbatim with its
// export attribute, never overwritten. Identical in normal and POSIX modes
// (oracle: GNU bash 5.3.15, both modes, absent/empty/nonempty matrix; see
// docs/make-shell-startup-oracle.md and scripts/make-shell-var-reducer.sh).

// loginShellPath returns the current user's login shell per bash's
// get_current_user_info: the passwd shell field for our uid, or "/bin/sh"
// when the lookup fails or the field is empty. Pure Go (no cgo) means the
// lookup reads /etc/passwd directly; on hosts whose users live only in a
// directory service (macOS Open Directory) the uid is absent from the file
// and the documented "/bin/sh" fallback applies — matching what GNU bash
// itself does whenever getpwuid returns no entry.
func loginShellPath() string {
	f, err := os.Open("/etc/passwd")
	if err != nil {
		return "/bin/sh"
	}
	defer f.Close()
	return loginShellFromPasswd(f, os.Getuid())
}

func loginShellFromPasswd(r io.Reader, uid int) string {
	want := strconv.Itoa(uid)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := sc.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// name:passwd:uid:gid:gecos:home:shell
		fields := strings.Split(line, ":")
		if len(fields) < 7 || fields[2] != want {
			continue
		}
		if shell := fields[6]; shell != "" {
			return shell
		}
		return "/bin/sh"
	}
	return "/bin/sh"
}

// startupShellEnviron overlays the synthesized non-exported SHELL over base.
type startupShellEnviron struct {
	base  expand.Environ
	shell expand.Variable
}

// withStartupShellVar applies bash's SHELL startup default. When base
// already carries SHELL — set to anything, including the empty string — it
// is returned untouched. Windows is left alone: GNU bash has no native
// Windows build to serve as an oracle, and synthesizing a unix path there
// would be speculative.
func withStartupShellVar(base expand.Environ) expand.Environ {
	if runtime.GOOS == "windows" || base.Get("SHELL").IsSet() {
		return base
	}
	return startupShellEnviron{
		base:  base,
		shell: expand.Variable{Set: true, Kind: expand.String, Str: loginShellPath()},
	}
}

func (e startupShellEnviron) Get(name string) expand.Variable {
	if name == "SHELL" {
		return e.shell
	}
	return e.base.Get(name)
}

func (e startupShellEnviron) Each(fn func(string, expand.Variable) bool) {
	keepGoing := true
	e.base.Each(func(name string, vr expand.Variable) bool {
		if name == "SHELL" {
			return true
		}
		keepGoing = fn(name, vr)
		return keepGoing
	})
	if keepGoing {
		fn("SHELL", e.shell)
	}
}
