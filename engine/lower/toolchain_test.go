package lower

import (
	"runtime"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

const independentMethodSource = "type R int\nfunc (r R) M[T any](v T) T {\n return v\n}\n"

func toolchainSource(t *testing.T, source string) *syntax.File {
	t.Helper()
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "capability.bpp")
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func TestToolchainVersionBoundary(t *testing.T) {
	for _, tc := range []struct {
		version   string
		supported bool
	}{
		{"go1.26.5", false}, {"go1.26.99", false}, {"go1.27rc1", false},
		{"go1.27.0", true}, {"go1.27.1", true}, {"go1.28.0", true},
		{"", false}, {"unknown", false}, {"devel go1.28-deadbeef", false},
	} {
		t.Run(tc.version, func(t *testing.T) {
			if got := supportsMethodTypeParams(tc.version); got != tc.supported {
				t.Fatalf("supportsMethodTypeParams(%q) = %v, want %v", tc.version, got, tc.supported)
			}
		})
	}
}

func TestToolchainPositionedCapabilityDiagnostic(t *testing.T) {
	file := toolchainSource(t, independentMethodSource)
	err := checkToolchainVersion(file, "go1.26.5")
	diagnostics, ok := err.(ErrorList)
	if !ok || len(diagnostics) != 1 {
		t.Fatalf("diagnostics = %#v", err)
	}
	d := diagnostics[0]
	want := "2:1: LOWER-ETOOLCHAIN: independent method type parameters require a compiler built with Go 1.27.0 or newer (built with go1.26.5)"
	if d.Error() != want || d.Node != "BashPPFuncDecl" {
		t.Fatalf("diagnostic = %#v (%s)", d, d.Error())
	}
	// A failed capability check does not mutate the positioned source tree.
	if err := checkToolchainVersion(file, "go1.27.0"); err != nil {
		t.Fatal(err)
	}
}

func TestToolchainLeavesOlderFeaturesAvailable(t *testing.T) {
	for _, source := range []string{
		"echo hello\n",
		"func identity[T any](v T) T {\n return v\n}\n",
		"type R int\nfunc (r R) M(v int) int {\n return v\n}\n",
		"type Box[T any] struct { Value T }\nfunc (b Box[T]) Get() T {\n return b.Value\n}\n",
	} {
		file := toolchainSource(t, source)
		for _, buildVersion := range []string{"go1.26.5", "unknown"} {
			if err := checkToolchainVersion(file, buildVersion); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestToolchainActualCompilerBuild(t *testing.T) {
	// This invokes the production entry point with the actual linked toolchain,
	// not an environment-variable substitution or a simulated version string.
	file := toolchainSource(t, independentMethodSource)
	err := CheckToolchain(file)
	t.Logf("actual compiler build: %s; capability diagnostic: %v", runtime.Version(), err)
	if supportsMethodTypeParams(runtime.Version()) {
		if err != nil {
			t.Fatal(err)
		}
	} else {
		diagnostics, ok := err.(ErrorList)
		if !ok || len(diagnostics) != 1 || diagnostics[0].Code != CodeToolchain {
			t.Fatalf("unsupported actual checker did not fail closed: %v", err)
		}
	}
}
