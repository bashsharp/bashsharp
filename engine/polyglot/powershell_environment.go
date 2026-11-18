package polyglot

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"slices"
	"sort"
	"strings"
)

var powerShellExecutionKeys = []string{"LANG", "LC_ALL", "LC_CTYPE", "TERM", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT"}

// powerShellEnvironmentEntry preserves empty versus absent inputs. Windows
// environment names are case-insensitive, including when supplied by embedders.
func powerShellEnvironmentEntry(env map[string]string, key string) (string, bool) {
	if value, ok := env[key]; ok {
		return value, true
	}
	if runtime.GOOS == "windows" {
		for name, value := range env {
			if strings.EqualFold(name, key) {
				return value, true
			}
		}
	}
	return "", false
}

// PowerShellExecutionEnvironment binds a compiled PowerShell or C# fence's admitted
// startup inputs once, before its worker starts. Absent inputs retain embedded
// defaults; explicitly empty inputs replace them. It never mutates the embedded
// plan, forwards other ambient variables, or re-resolves build-time assets.
// Interpreted fences already carry their request snapshot and do not call this.
func PowerShellExecutionEnvironment(base *EnvironmentPlan) *EnvironmentPlan {
	return powerShellExecutionEnvironment(base, os.Environ())
}

func powerShellExecutionEnvironment(base *EnvironmentPlan, environ []string) *EnvironmentPlan {
	if base == nil {
		return nil
	}
	plan := base.Clone()
	env := map[string]string{}
	for _, entry := range environ {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	for _, key := range powerShellExecutionKeys {
		if value, present := powerShellEnvironmentEntry(env, key); present {
			plan.Env = slices.DeleteFunc(plan.Env, func(entry string) bool {
				name, _, _ := strings.Cut(entry, "=")
				return name == key || runtime.GOOS == "windows" && strings.EqualFold(name, key)
			})
			plan.Env = append(plan.Env, key+"="+value)
		}
	}
	sort.Strings(plan.Env)
	if !slices.Equal(plan.Env, base.Env) {
		// Bind the new launch snapshot to the original content-addressed build plan.
		// Re-reading its files here would make a deployed artifact depend on source
		// and manifest paths that need not exist on the execution host.
		sum := sha256.Sum256([]byte("powershell-execution-v1\x00" + base.Fingerprint + "\x00" + strings.Join(plan.Env, "\x00")))
		plan.Fingerprint = hex.EncodeToString(sum[:])
	}
	return &plan
}
