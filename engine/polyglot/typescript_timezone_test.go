package polyglot

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This tests planning and launch configuration without running Node or fetching
// a compiler. The product-level companion fixture exercises the actual worker.
func TestTypeScriptExplicitTimezoneEnvironment(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "node.exe")
	writeEnvironmentFile(t, executable, "fixture runtime")
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), "{}")
	t.Setenv("TZ", "ambient-must-not-leak")
	for _, language := range []string{"typescript", "javascript"} {
		t.Run(language, func(t *testing.T) {
			fingerprints := map[string]string{}
			for _, tc := range []struct {
				name, value string
				present     bool
			}{
				{name: "absent"},
				{name: "empty", present: true},
				{name: "utc", value: "UTC", present: true},
				{name: "offset", value: "Etc/GMT+12", present: true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					environ := []string{"PATH=", "BASHPP_NODE=" + executable,
						"BASHPP_TYPESCRIPT_MODULE=" + filepath.Join(root, "typescript.js"),
						"NODE_OPTIONS=--require=untrusted", "NODE_PATH=untrusted",
						"API_TOKEN=private", "UNRELATED=private"}
					if tc.present {
						environ = append(environ, "TZ="+tc.value)
					}
					plan, err := DiscoverEnvironment(EnvironmentRequest{
						Source: filepath.Join(root, "source.bsh"), Language: language, Environ: environ,
					})
					if err != nil {
						t.Fatal(err)
					}
					fingerprints[tc.name] = plan.Fingerprint
					cmd := exec.Command("not-executed")
					(TypeScript{Environment: &plan, JavaScript: language == "javascript"}).configure(cmd)
					got := map[string]string{}
					for _, entry := range cmd.Env {
						key, value, _ := strings.Cut(entry, "=")
						got[key] = value
					}
					value, present := got["TZ"]
					if present != tc.present || value != tc.value {
						t.Errorf("TZ = (%q, %v), want (%q, %v)", value, present, tc.value, tc.present)
					}
					for _, key := range []string{"NODE_OPTIONS", "NODE_PATH", "API_TOKEN", "UNRELATED"} {
						if _, ok := got[key]; ok {
							t.Errorf("unapproved environment key %s propagated", key)
						}
					}
				})
			}
			seen := map[string]string{}
			for name, fingerprint := range fingerprints {
				if previous, ok := seen[fingerprint]; ok {
					t.Errorf("timezone states %s and %s share a fingerprint", previous, name)
				}
				seen[fingerprint] = name
			}
		})
	}
}
