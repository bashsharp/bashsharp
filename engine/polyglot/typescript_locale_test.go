package polyglot

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTypeScriptExplicitLocaleEnvironment(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "node.exe")
	writeEnvironmentFile(t, executable, "fixture runtime")
	writeEnvironmentFile(t, filepath.Join(root, "package.json"), "{}")
	for _, language := range []string{"typescript", "javascript"} {
		t.Run(language, func(t *testing.T) {
			for _, key := range []string{"LANG", "LC_ALL", "LC_CTYPE"} {
				t.Run(key, func(t *testing.T) {
					fingerprints := map[string]string{}
					for _, tc := range []struct {
						name, value string
						present     bool
					}{
						{name: "absent"},
						{name: "empty", present: true},
						{name: "c", value: "C.UTF-8", present: true},
						{name: "turkish", value: "tr_TR.UTF-8", present: true},
					} {
						t.Run(tc.name, func(t *testing.T) {
							environ := []string{"PATH=", "BASHPP_NODE=" + executable,
								"BASHPP_TYPESCRIPT_MODULE=" + filepath.Join(root, "typescript.js"),
								"NODE_OPTIONS=--require=untrusted", "NODE_PATH=untrusted",
								"API_TOKEN=private", "UNRELATED=private"}
							if tc.present {
								environ = append(environ, key+"="+tc.value)
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
								name, value, _ := strings.Cut(entry, "=")
								got[name] = value
							}
							value, present := got[key]
							if present != tc.present || value != tc.value {
								t.Errorf("%s = (%q, %v), want (%q, %v)", key, value, present, tc.value, tc.present)
							}
							for _, excluded := range []string{"NODE_OPTIONS", "NODE_PATH", "API_TOKEN", "UNRELATED"} {
								if _, ok := got[excluded]; ok {
									t.Errorf("unapproved environment key %s propagated", excluded)
								}
							}
						})
					}
					seen := map[string]string{}
					for name, fingerprint := range fingerprints {
						if previous, ok := seen[fingerprint]; ok {
							t.Errorf("%s states %s and %s share a fingerprint", key, previous, name)
						}
						seen[fingerprint] = name
					}
				})
			}
		})
	}
}
