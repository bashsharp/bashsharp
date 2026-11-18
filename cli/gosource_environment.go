package cli

import (
	"os"
	"strings"
)

const (
	hardIgnoreEnvironment         = "BASHY_HARD_IGNORE"
	launcherHardIgnoreOriginalEnv = "BASHY_LAUNCHER_HARD_IGNORE_ORIGINAL"
)

// agentos imports cli, so cli's package variables initialize before agentos
// installs its shell capability manifest. Preserve the cold caller's original
// environment, including order and explicitly supplied shell/agent variables.
// Shell startup still uses its normal enriched environment; only GoSource
// dependency processes receive this separate immutable starting snapshot.
var goSourceProcessEnvironment = snapshotGoSourceProcessEnvironment()

// snapshotGoSourceProcessEnvironment removes the native signal launcher's
// private provenance and restores the caller's BASHY_HARD_IGNORE entry. The
// launcher must enrich the real shell environment before the Go runtime starts,
// but that enrichment is not part of the environment of an interpreted Go
// program. Preserve both absence and an explicitly supplied value, in place.
func snapshotGoSourceProcessEnvironment() []string {
	env := os.Environ()
	original, launched := os.LookupEnv(launcherHardIgnoreOriginalEnv)
	if !launched {
		return env
	}
	_ = os.Unsetenv(launcherHardIgnoreOriginalEnv)

	hadOriginal := strings.HasPrefix(original, "1")
	original = strings.TrimPrefix(original, "1")
	result := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case launcherHardIgnoreOriginalEnv:
			continue
		case hardIgnoreEnvironment:
			if hadOriginal {
				result = append(result, hardIgnoreEnvironment+"="+original)
			}
			continue
		default:
			result = append(result, entry)
		}
	}
	return result
}

// AdoptGoSourceProcessEnvironment records the restored environment of an
// owned child frame. Ordinary starts deliberately retain the package's cold
// caller snapshot: AgentOS initialization may install its own variables
// before Main runs.
func AdoptGoSourceProcessEnvironment() {
	goSourceProcessEnvironment = os.Environ()
}
