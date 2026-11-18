package interp

import (
	"slices"
	"strings"

	"github.com/bashsharp/bashsharp/engine/expand"
)

// GoSourceEnv supplies the original Go program's process environment in caller
// order. The slice is copied. It applies to GoSource dependency processes and
// explicitly invoked Go fence commands; Bash variables, shell subprocesses,
// and typed Go fence calls retain their usual behavior.
// An empty slice means an empty environment. Without this option, exported
// variables in the initial Runner.Env are used, in its iteration order.
// Reset retains this configuration; os.Setenv/Unsetenv in the Go program change
// its persistent dependency process only, not this starting snapshot.
func GoSourceEnv(env []string) RunnerOption {
	snapshot := make([]string, 0, len(env))
	for _, entry := range env {
		name, _, _ := strings.Cut(entry, "=")
		if strings.EqualFold(name, reexecPreparedCacheEnv) || strings.EqualFold(name, reexecInterpreterIDEnv) {
			continue
		}
		snapshot = append(snapshot, entry)
	}
	return func(r *Runner) error { r.goSourceEnvironment = slices.Clone(snapshot); return nil }
}

func (r *Runner) bashPPGoSourceEnvironment() []string {
	if r.goSourceEnvironment != nil {
		return slices.Clone(r.goSourceEnvironment)
	}
	// A nonnil empty slice is essential: exec.Cmd interprets nil as inheritance
	// from the host process, which would leak unrelated embedding-process state.
	env := []string{}
	positions := make(map[string]int)
	if r.Env != nil {
		r.Env.Each(func(name string, value expand.Variable) bool {
			// Environ permits repeated names; the final occurrence determines
			// the value and export attribute, including an unset override.
			if previous, ok := positions[name]; ok {
				env[previous] = ""
				delete(positions, name)
			}
			if value.IsSet() && value.Exported {
				positions[name] = len(env)
				env = append(env, name+"="+value.String())
			}
			return true
		})
	}
	return slices.DeleteFunc(env, func(entry string) bool { return entry == "" })
}

// bashPPGoCommandEnvironment keeps the exact caller snapshot for a Go command
// and overlays only exports or unsets made by the running shell. The ordinary
// shell environment contains startup additions that were never in that
// snapshot, so passing execEnv directly would expose those to the guest.
func (r *Runner) bashPPGoCommandEnvironment() []string {
	current := execEnv(r.writeEnv)
	if r.goSourceEnvironment == nil {
		return current
	}
	overlay, ok := r.writeEnv.(*overlayEnviron)
	if !ok {
		return current
	}
	touched := overlay.goCommandTouchedNames()
	values := make(map[string]string, len(touched))
	for name := range touched {
		variable := r.writeEnv.Get(name)
		if variable.IsSet() && variable.Exported {
			values[name] = name + "=" + execEnvValue(variable)
		}
	}
	result := make([]string, 0, len(r.goSourceEnvironment)+len(touched))
	seen := make(map[string]bool, len(touched))
	for _, entry := range r.goSourceEnvironment {
		name, _, ok := strings.Cut(entry, "=")
		if !ok || !touched[name] {
			result = append(result, entry)
			continue
		}
		if !seen[name] {
			if replacement, set := values[name]; set {
				result = append(result, replacement)
			}
			seen[name] = true
		}
	}
	for _, entry := range current {
		name, _, ok := strings.Cut(entry, "=")
		if ok && touched[name] && !seen[name] {
			if replacement, set := values[name]; set {
				result = append(result, replacement)
			}
			seen[name] = true
		}
	}
	return result
}
