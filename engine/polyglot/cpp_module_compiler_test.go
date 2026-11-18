package polyglot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestCPPModuleDriverTraceRefusals(t *testing.T) {
	compiler, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quote := strconv.Quote
	valid := quote(compiler) + ` "-cc1" "-emit-obj" "-triple" "x86_64-unknown-linux-gnu" "-std=c++20" "-o" "asset.o" "-x" "c++" "asset.cppm"`
	args, err := moduleBMIArgs(valid, compiler, "asset.cppm", "asset.o", "asset.pcm")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-emit-module-interface") || !strings.Contains(joined, "-o asset.pcm") || !strings.Contains(joined, "-triple x86_64-unknown-linux-gnu") {
		t.Fatalf("argv=%v", args)
	}
	for name, trace := range map[string]string{
		"missing":    "driver version",
		"multiple":   valid + "\n" + valid,
		"executable": strings.Replace(valid, quote(compiler), quote(filepath.Join(t.TempDir(), "other")), 1),
		"action":     strings.Replace(valid, `"-emit-obj"`, `"-emit-pch"`, 1),
		"source":     strings.Replace(valid, `"asset.cppm"`, `"other.cppm"`, 1),
		"output":     strings.Replace(valid, `"asset.o"`, `"other.o"`, 1),
		"unquoted":   strings.Replace(valid, `"-triple"`, `-triple`, 1),
		"validation": strings.Replace(valid, `"-triple"`, `"-fno-validate-pch" "-triple"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := moduleBMIArgs(trace, compiler, "asset.cppm", "asset.o", "asset.pcm"); err == nil {
				t.Fatal("accepted invalid trace")
			}
		})
	}
}

func TestCPPModuleCompilerIdentity(t *testing.T) {
	unit := NativeModuleUnit{Name: "Example", Kind: "interface"}
	info := "  Interface Unit 'Example' is the Primary Module at index #1\n  Module name: Example\n"
	if err := moduleInfoMatches(info, unit); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.ReplaceAll(info, "Example", "Wrong"), strings.Replace(info, "Interface Unit", "Partition Implementation", 1), ""} {
		if err := moduleInfoMatches(bad, unit); err == nil {
			t.Fatal("accepted mismatched compiler identity")
		}
	}
}

func cppModuleTestEnvironment(t *testing.T, root string) EnvironmentPlan {
	t.Helper()
	zig := os.Getenv("BASHPP_TEST_ZIG")
	if zig == "" {
		var err error
		zig, err = exec.LookPath("zig")
		if err != nil {
			t.Skip("pinned Zig module provider unavailable")
		}
	}
	executable, err := filepath.Abs(zig)
	if err != nil {
		t.Fatal(err)
	}
	return EnvironmentPlan{Language: "cpp", Root: root, Dir: root, SourceDir: root, Executable: executable, ExecutableArgs: []string{"c++"}, Env: nativeLaunchEnvironment(envMap(os.Environ()))}
}

func TestCPPModuleAssetArtifact(t *testing.T) {
	for _, tc := range []struct {
		name     string
		units    []NativeModuleUnit
		sources  []string
		consumer string
	}{
		{"primary", []NativeModuleUnit{{Name: "Example", Kind: "interface", Source: "example.cppm"}}, []string{"export module Example;\nnamespace Example_NS { int internal() { return 42; } export int f() { return internal(); } }\n"}, "import Example;\nint example_value() { return Example_NS::f(); }\n"},
		{"interface-partition", []NativeModuleUnit{{Name: "Example:part", Kind: "interface", Source: "part.cppm"}, {Name: "Example", Kind: "interface", Source: "example.cppm", Requires: []string{"Example:part"}}}, []string{"export module Example:part;\nexport int answer() { return 42; }\n", "export module Example;\nexport import :part;\n"}, "import Example;\nint example_value() { return answer(); }\n"},
		{"internal-partition", []NativeModuleUnit{{Name: "Example:part", Kind: "internal-partition", Source: "part.cppm"}, {Name: "Example", Kind: "interface", Source: "example.cppm", Requires: []string{"Example:part"}}}, []string{"module Example:part;\nint internal() { return 42; }\n", "export module Example;\nimport :part;\nexport int answer() { return internal(); }\n"}, "import Example;\nint example_value() { return answer(); }\n"},
		{"implementation", []NativeModuleUnit{{Name: "Example", Kind: "interface", Source: "example.cppm"}, {Name: "Example", Kind: "implementation", Source: "impl.cpp", Requires: []string{"Example"}}}, []string{"export module Example;\nexport int answer();\n", "module Example;\nint answer() { return 42; }\n"}, "import Example;\nint example_value() { return answer(); }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			environment := cppModuleTestEnvironment(t, root)
			for i := range tc.units {
				writeModuleTestFile(t, root, tc.units[i].Source, tc.sources[i])
				tc.units[i].Source = filepath.Join(root, tc.units[i].Source)
			}
			environment.NativeModules = tc.units
			analyzer := CPP{Environment: &environment}
			plans, err := Prepare(context.Background(), []Block{{Language: "cpp", Source: tc.consumer}}, map[string]Analyzer{"cpp": analyzer})
			if err != nil {
				t.Fatal(err)
			}
			if len(plans) != 1 || len(plans[0].Exports) != 1 || plans[0].Exports[0].Name != "example_value" {
				t.Fatalf("exports=%v", plans)
			}
			for _, unit := range tc.units {
				if err := os.Remove(unit.Source); err != nil {
					t.Fatal(err)
				}
			}
			module := Start(plans[0], analyzer)
			defer module.Close()
			got, err := module.Call(context.Background(), "example_value")
			if err != nil || got.Value != int64(42) || got.Stdout != "" || got.Stderr != "" {
				t.Fatalf("call=%#v,error=%v", got, err)
			}
		})
	}
}

func TestCPPModuleCompileRefusals(t *testing.T) {
	for _, tc := range []struct{ name, source, consumer, kind string }{
		{"name", "export module Wrong;\nexport int answer(){return 42;}", "import Example;\nint value(){return answer();}", "interface"},
		{"kind", "module Example:part;\nint answer(){return 42;}", "int value(){return 42;}", "interface"},
		{"missing-object", "export module Example;\nexport int answer();", "import Example;\nint value(){return answer();}", "interface"},
		{"private", "export module Example;\nint hidden(){return 42;}\nexport int answer(){return hidden();}", "import Example;\nint value(){return hidden();}", "interface"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			environment := cppModuleTestEnvironment(t, root)
			writeModuleTestFile(t, root, "example.cppm", tc.source)
			name := "Example"
			if tc.name == "kind" {
				name = "Example:part"
			}
			environment.NativeModules = []NativeModuleUnit{{Name: name, Kind: tc.kind, Source: filepath.Join(root, "example.cppm")}}
			if _, _, err := (CPP{Environment: &environment}).AnalyzeArtifact(context.Background(), tc.consumer); err == nil {
				t.Fatal(fmt.Sprintf("accepted %s", tc.name))
			}
		})
	}
}
