package polyglot

import (
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestPowerShellExplicitLocaleEnvironment(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pwsh.exe")
	writeEnvironmentFile(t, executable, "fixture runtime")
	for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TERM", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT"} {
		t.Run(key, func(t *testing.T) {
			t.Setenv(key, "ambient-must-not-leak")
			fingerprints := map[string]bool{}
			for _, value := range []string{"absent", "", "en_US.UTF-8", "C.UTF-8"} {
				env := []string{"PATH=", "BASHPP_PWSH=" + executable, "PRIVATE_TOKEN=secret", "PSModuleAnalysisCachePath=untrusted"}
				if value != "absent" {
					env = append(env, key+"="+value)
				}
				plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "input.bsh"), Language: "powershell", Environ: env})
				if err != nil {
					t.Fatal(err)
				}
				cmd := exec.Command("not-executed")
				(PowerShell{Environment: &plan}).configure(cmd)
				present := slices.Contains(cmd.Env, key+"="+value)
				if value != "absent" && !present {
					t.Fatalf("lost explicit %s=%q: %v", key, value, cmd.Env)
				}
				for _, entry := range cmd.Env {
					if strings.Contains(entry, "ambient-must-not-leak") || strings.HasPrefix(entry, "PRIVATE_TOKEN=") || strings.HasPrefix(entry, "PSModuleAnalysisCachePath=") {
						t.Fatalf("unadmitted input: %s", entry)
					}
				}
				if fingerprints[plan.Fingerprint] {
					t.Fatalf("%s state %q did not change fingerprint", key, value)
				}
				fingerprints[plan.Fingerprint] = true
				clone := plan.Clone()
				cmd.Env[0] = "mutated"
				env[0] = "mutated"
				if !reflect.DeepEqual(plan, clone) {
					t.Fatal("launch/request mutated plan")
				}
			}
		})
	}
}

func TestPowerShellExecutionEnvironment(t *testing.T) {
	for _, language := range []string{"powershell", "csharp"} {
		t.Run(language, func(t *testing.T) {
			base := &EnvironmentPlan{Language: language, Fingerprint: "build-plan", Env: []string{"DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=1", "LANG=C", "LC_ALL=C", "LC_CTYPE=C", "TERM=dumb"}}
			before := base.Clone()
			for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE", "TERM", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT"} {
				for _, value := range []string{"", "explicit"} {
					got := powerShellExecutionEnvironment(base, []string{key + "=" + value, "PRIVATE_TOKEN=secret", "PATH=untrusted"})
					if !slices.Contains(got.Env, key+"="+value) || got.Fingerprint == base.Fingerprint {
						t.Fatalf("override lost: %#v", got)
					}
					if !reflect.DeepEqual(*base, before) {
						t.Fatal("mutated embedded defaults")
					}
					for _, entry := range got.Env {
						if strings.Contains(entry, "secret") || strings.Contains(entry, "untrusted") {
							t.Fatalf("ambient leak: %s", entry)
						}
					}
					again := powerShellExecutionEnvironment(base, []string{key + "=" + value})
					if again.Fingerprint != got.Fingerprint {
						t.Fatal("unadmitted input changed fingerprint")
					}
				}
			}
			got := powerShellExecutionEnvironment(base, []string{"UNRELATED=x"})
			if !reflect.DeepEqual(*got, before) {
				t.Fatalf("absent overrides changed defaults: %#v", got)
			}
			got.Env[0] = "mutated"
			if !reflect.DeepEqual(*base, before) {
				t.Fatal("clone aliases embedded defaults")
			}
			if runtime.GOOS == "windows" {
				got := powerShellExecutionEnvironment(base, []string{"lang=en_US.UTF-8", "lc_ctype=de_DE.UTF-8", "term="})
				if !slices.Contains(got.Env, "LANG=en_US.UTF-8") || !slices.Contains(got.Env, "LC_CTYPE=de_DE.UTF-8") || !slices.Contains(got.Env, "TERM=") {
					t.Fatal(got.Env)
				}
			}
		})
	}
}

func TestPowerShellExplicitInvariantOverridesProvisioner(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "pwsh.exe")
	writeEnvironmentFile(t, executable, "fixture runtime")
	saved := ToolResolver
	t.Cleanup(func() { ToolResolver = saved })
	t.Setenv("DOTNET_SYSTEM_GLOBALIZATION_INVARIANT", "1")
	ToolResolver = func(string) ([]string, string, error) { return []string{executable}, "fixture provisioner", nil }
	for _, value := range []string{"0", ""} {
		plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "input.bsh"), Language: "powershell", Environ: []string{"PATH=", "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT=" + value}})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(plan.Env, "DOTNET_SYSTEM_GLOBALIZATION_INVARIANT="+value) {
			t.Fatalf("provisioner overwrote explicit recipe: %v", plan.Env)
		}
	}
}
