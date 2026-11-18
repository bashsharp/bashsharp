// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package cli

import (
	"fmt"
	"os"
	"strings"
)

type startupCredentialState struct {
	realUID, effectiveUID int
	realGID, effectiveGID int
}

// startupPrivilegedMode records the secure-startup behavior selected before
// credentials are normalized. It remains true after a successful ID drop so
// startup files and privileged environment inputs stay suppressed.
var startupPrivilegedMode bool

func prepareStartupCredentials(privileged bool) error {
	state, ok := currentStartupCredentials()
	mismatch := ok && (state.realUID != state.effectiveUID || state.realGID != state.effectiveGID)
	startupPrivilegedMode = privileged || mismatch
	if !ok {
		return nil
	}
	return normalizeStartupCredentials(state, privileged, setStartupGID, setStartupUID)
}

// normalizeStartupCredentials mirrors bash startup for a shell entered through
// a set-id/session helper. Unless invocation privileged mode (-p) is active,
// bash discards an effective identity which differs from the caller's real
// identity. Drop the group before the user so losing an elevated user cannot
// strand an elevated group. A failed drop must stop shell startup.
func normalizeStartupCredentials(state startupCredentialState, privileged bool, setGID, setUID func(int) error) error {
	if privileged {
		return nil
	}
	if state.effectiveGID != state.realGID {
		if err := setGID(state.realGID); err != nil {
			return fmt.Errorf("drop effective gid to %d: %w", state.realGID, err)
		}
	}
	if state.effectiveUID != state.realUID {
		if err := setUID(state.realUID); err != nil {
			return fmt.Errorf("drop effective uid to %d: %w", state.realUID, err)
		}
	}
	return nil
}

// commandLinePrivilegedMode resolves the last invocation -p/+p after the argv
// normalizer has expanded both forms to -o/-bashy-plus-o. Script operands do
// not affect shell startup options.
func commandLinePrivilegedMode() bool {
	state := false
	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--":
			return state
		case "-o", "-bashy-plus-o":
			if i+1 >= len(os.Args) {
				continue
			}
			on := os.Args[i] == "-o"
			i++
			if os.Args[i] == "privileged" {
				state = on
			}
		case "-c":
			return state
		default:
			if !strings.HasPrefix(os.Args[i], "-") {
				return state
			}
		}
	}
	return state
}

func secureStartupEnv(env []string) []string {
	if !startupPrivilegedMode {
		return env
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if name == "SHELLOPTS" || name == "BASHOPTS" || name == "CDPATH" || name == "GLOBIGNORE" {
			continue
		}
		if strings.HasPrefix(name, "BASH_FUNC_") && strings.HasSuffix(name, "%%") {
			continue
		}
		out = append(out, entry)
	}
	return out
}
