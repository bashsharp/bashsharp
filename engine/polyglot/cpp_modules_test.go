package polyglot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeModuleTestFile(t *testing.T, root, name, data string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCPPModuleManifestValidation(t *testing.T) {
	valid := `{"version":1,"units":[{"name":"Example","kind":"interface","source":"example.cppm","requires":[]}]}`
	tests := []struct{ name, manifest, want string }{
		{"valid", valid, ""},
		{"version", strings.Replace(valid, `"version":1`, `"version":2`, 1), "version"},
		{"unknown field", strings.Replace(valid, `"version":1`, `"command":"echo bad","version":1`, 1), "unknown field"},
		{"trailing", valid + ` {}`, "trailing"},
		{"reserved", strings.Replace(valid, `"Example"`, `"std.compat"`, 1), "reserved"},
		{"invalid name", strings.Replace(valid, `"Example"`, `"-bad"`, 1), "invalid"},
		{"kind", strings.Replace(valid, `"interface"`, `"command"`, 1), "kind"},
		{"missing source", strings.Replace(valid, `example.cppm`, `absent.cppm`, 1), "asset"},
		{"outside", strings.Replace(valid, `example.cppm`, `../outside.cppm`, 1), "escapes"},
		{"absolute", strings.Replace(valid, `example.cppm`, `/outside.cppm`, 1), "relative"},
		{"missing dependency", strings.Replace(valid, `"requires":[]`, `"requires":["Missing"]`, 1), "missing provider"},
		{"cycle", strings.Replace(valid, `"requires":[]`, `"requires":["Example"]`, 1), "cycle"},
		{"internal name", strings.Replace(valid, `"interface"`, `"internal-partition"`, 1), "partition name"},
		{"implementation dependency", strings.Replace(valid, `"interface"`, `"implementation"`, 1), "must require"},
		{"empty", `{"version":1,"units":[]}`, "1 to 256"},
		{"duplicate provider", `{"version":1,"units":[{"name":"Example","kind":"interface","source":"example.cppm"},{"name":"Example","kind":"interface","source":"second.cppm"}]}`, "duplicate C++ module provider"},
		{"duplicate source", `{"version":1,"units":[{"name":"Example","kind":"interface","source":"example.cppm"},{"name":"Other","kind":"interface","source":"example.cppm"}]}`, "duplicate C++ module source"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "project")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			writeModuleTestFile(t, parent, "outside.cppm", "export module Outside;")
			writeModuleTestFile(t, root, "example.cppm", "export module Example;")
			writeModuleTestFile(t, root, "second.cppm", "export module Other;")
			writeModuleTestFile(t, root, "modules.json", tt.manifest)
			units, files, err := readNativeModules(root, root, "modules.json")
			if tt.want != "" {
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("error = %v, want %q", err, tt.want)
				}
				return
			}
			if err != nil || len(units) != 1 || len(files) != 2 || !filepath.IsAbs(units[0].Source) {
				t.Fatalf("units=%v files=%v err=%v", units, files, err)
			}
		})
	}
}

func TestCPPModuleGraphAndClone(t *testing.T) {
	root := t.TempDir()
	units := []NativeModuleUnit{
		{Name: "Example", Kind: "implementation", Source: "impl.cpp", Requires: []string{"Example"}},
		{Name: "Example", Kind: "interface", Source: "main.cppm", Requires: []string{"Example:part"}},
		{Name: "Example:part", Kind: "interface", Source: "part.cppm"},
	}
	for _, u := range units {
		writeModuleTestFile(t, root, u.Source, "// exact asset bytes\n")
	}
	data, err := json.Marshal(nativeModuleManifest{Version: 1, Units: units})
	if err != nil {
		t.Fatal(err)
	}
	writeModuleTestFile(t, root, "modules.json", string(data))
	got, _, err := readNativeModules(root, root, "modules.json")
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Name != "Example:part" || got[1].Kind != "interface" || got[2].Kind != "implementation" {
		t.Fatalf("order=%v", got)
	}
	plan := EnvironmentPlan{NativeModules: got}
	clone := plan.Clone()
	clone.NativeModules[1].Requires[0] = "mutated"
	clone.NativeModules[0].Name = "mutated"
	if plan.NativeModules[1].Requires[0] != "Example:part" || plan.NativeModules[0].Name != "Example:part" {
		t.Fatal("clone aliases module graph")
	}
}

func TestCPPModuleAssetSymlinkEscape(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "project")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	writeModuleTestFile(t, parent, "outside.cppm", "export module Outside;")
	if err := os.Symlink(filepath.Join(parent, "outside.cppm"), filepath.Join(root, "link.cppm")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := moduleAsset(root, root, "link.cppm"); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("escape=%v", err)
	}
}

func TestCPPModuleOverlayFormats(t *testing.T) {
	for _, data := range []string{
		"language: cpp\nruntime: clang++\nnative_modules: modules.json\n",
		"environments:\n  native:\n    language: cpp\n    runtime: clang++\n    native_modules: modules.json\n",
	} {
		got, err := parseOverlayYAML([]byte(data), "bashpp.yaml")
		if err != nil || len(got) != 1 || got[0].NativeModules != "modules.json" {
			t.Fatalf("YAML=%v,%v", got, err)
		}
	}
	var raw any
	if err := json.Unmarshal([]byte(`{"language":"cpp","runtime":"clang++","native_modules":"modules.json"}`), &raw); err != nil {
		t.Fatal(err)
	}
	got, err := overlayObjects(raw, "bashpp.json")
	if err != nil || len(got) != 1 || got[0].NativeModules != "modules.json" {
		t.Fatalf("JSON=%v,%v", got, err)
	}
}

func TestCPPModuleDiscoveryFingerprint(t *testing.T) {
	root := t.TempDir()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	overlay, _ := json.Marshal(map[string]string{"language": "cpp", "runtime": executable, "native_modules": "modules.json"})
	writeModuleTestFile(t, root, "bashpp.json", string(overlay))
	writeModuleTestFile(t, root, "modules.json", `{"version":1,"units":[{"name":"Example","kind":"interface","source":"example.cppm"}]}`)
	writeModuleTestFile(t, root, "example.cppm", "export module Example;\n")
	request := EnvironmentRequest{Source: filepath.Join(root, "entry.bpp"), Language: "cpp", Environ: []string{"PATH="}}
	first, err := DiscoverEnvironment(request)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.NativeModules) != 1 || len(first.ResolutionFiles) != 3 {
		t.Fatalf("plan=%+v", first)
	}
	writeModuleTestFile(t, root, "example.cppm", "export module Example;\nexport int answer(){return 42;}\n")
	second, err := DiscoverEnvironment(request)
	if err != nil {
		t.Fatal(err)
	}
	if first.Fingerprint == second.Fingerprint {
		t.Fatal("module source change did not invalidate fingerprint")
	}
}
