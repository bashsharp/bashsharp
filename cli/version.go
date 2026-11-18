// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"os"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// bashVersion identifies bashy as a Bash 5.3 compatible shell. It is a var,
// not a const, so a release build can stamp in a real tag/build string via
//
//	go build -ldflags "-X main.bashVersion=5.3.0(1)-bashy-v0.1.0"
//
// The "5.3.0(1)-" prefix is what the bash test suite's fixtures key on; keep
// it when overriding so compliance checks still match. BASH_VERSION carries
// only "5.3.0(1)-bashy" (bashCompatVersion); the release suffix is shown by
// --version.
var bashVersion = "5.3.0(1)-bashy"

// buildID is stamped by the Makefile from Git metadata. It is intentionally
// separate from bashVersion so the Bash-compatible version value keeps the
// exact shape parsed by fixtures and scripts. Source tarballs without .git leave
// this empty.
var buildID string

// VersionProduct and VersionCompatibility control only the human-facing
// --version banner. The pure cmd/bash binary keeps the GNU-compatible banner;
// cmd/bashy overrides these labels so the AgentOS product does not claim to be
// GNU Bash. BASH_VERSION remains the compatibility version in both binaries.
var (
	VersionProduct       = "GNU bash"
	VersionCompatibility string
)

const (
	bashVerMajor = "5"
	bashVerMinor = "3"
	bashVerPatch = "0"
)

// BashyVersion returns bashy's own release version ("0.9.1") extracted
// from the stamped bashVersion suffix ("5.3.0(1)-bashy-v0.9.1"), or ""
// for an unstamped/dev build. Consumers (the skills host-version probe)
// treat "" as "omit".
func BashyVersion() string {
	_, v, ok := strings.Cut(bashVersion, "-bashy-v")
	if !ok || v == "" || v == "dev" {
		return ""
	}
	return v
}

// BuildID returns the stamped Git build identifier, usually an exact release tag
// or a short commit SHA with an optional "-dirty" suffix.
func BuildID() string {
	return strings.TrimSpace(buildID)
}

func bashVersionLine() string {
	if id := BuildID(); id != "" {
		return bashVersion + " (" + id + ")"
	}
	return bashVersion
}

func versionBanner() string {
	prefix := VersionProduct
	if VersionCompatibility != "" {
		prefix += ", " + VersionCompatibility
	}
	return prefix + ", version " + bashVersionLine()
}

// bashVersionVars returns the environment variables that identify bashy
// as a Bash 5.3 compatible shell.
func bashVersionVars() []string {
	exe, _ := os.Executable()
	if exe == "" {
		exe = "bashy"
	}
	return []string{
		"BASH=" + exe,
		"BASH_VERSION=" + bashCompatVersion(),
	}
}

// bashCompatVersion is the BASH_VERSION value: the stamp without bashy's own
// release suffix ("5.3.0(1)-bashy-v1.0.0" -> "5.3.0(1)-bashy"). GNU Bash's
// value has no dot after the patch level, and scripts (and the Bash 5.3
// history fixture) take ${BASH_VERSION%.*} expecting "5.3"; a dotted release
// suffix broke that. The full stamp stays in --version and BashyVersion.
func bashCompatVersion() string {
	if i := strings.Index(bashVersion, "-bashy"); i >= 0 {
		return bashVersion[:i] + "-bashy"
	}
	return bashVersion
}

// bashVersionEnviron overlays bashy's BASH and BASH_VERSION values without
// changing their inherited export attributes. GNU Bash binds both as shell
// variables during startup: they are visible to shell code but absent from a
// child's environment unless the caller had already exported them.
type bashVersionEnviron struct {
	base expand.Environ
	bash expand.Variable
	ver  expand.Variable
	last expand.Variable
}

func withBashVersionVars(base expand.Environ, inherited []string) expand.Environ {
	values := bashVersionVars()
	_, bash, _ := strings.Cut(values[0], "=")
	_, ver, _ := strings.Cut(values[1], "=")
	// GNU Bash preserves an inherited `_`; when it is absent, startup binds
	// it to the shell executable without exporting the synthesized value.
	last := base.Get("_")
	if !last.IsSet() {
		exe, _ := os.Executable()
		if exe == "" {
			exe = os.Args[0]
		}
		last = expand.Variable{Set: true, Kind: expand.String, Str: exe}
	}
	return bashVersionEnviron{
		base: base,
		bash: expand.Variable{
			Set: true, Exported: hasEnvKey(inherited, "BASH"), Kind: expand.String, Str: bash,
		},
		ver: expand.Variable{
			Set: true, Exported: hasEnvKey(inherited, "BASH_VERSION"), Kind: expand.String, Str: ver,
		},
		last: last,
	}
}

func (e bashVersionEnviron) Get(name string) expand.Variable {
	switch name {
	case "BASH":
		return e.bash
	case "BASH_VERSION":
		return e.ver
	case "_":
		return e.last
	default:
		return e.base.Get(name)
	}
}

func (e bashVersionEnviron) Each(fn func(string, expand.Variable) bool) {
	keepGoing := true
	e.base.Each(func(name string, vr expand.Variable) bool {
		if name == "BASH" || name == "BASH_VERSION" || name == "_" {
			return true
		}
		keepGoing = fn(name, vr)
		return keepGoing
	})
	if !keepGoing || !fn("BASH", e.bash) {
		return
	}
	if !fn("BASH_VERSION", e.ver) {
		return
	}
	fn("_", e.last)
}
