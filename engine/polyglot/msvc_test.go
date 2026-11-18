package polyglot

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestMSVCDiscoveryFromVSWhere(t *testing.T) {
	// Assembly is driven from fake vswhere output: trailing CRLF and a stray
	// second line (an installer warning, say) must not leak into the path.
	d := msvcDiscovery{
		VSWhereOut:     "C:\\VS\\2022\\BuildTools\r\nnoise\r\n",
		VCToolsVersion: "14.44.35207\r\n",
		SDKRoot:        filepath.Join("C:", "Kits", "10"),
		SDKVersions:    []string{"10.0.19041.0", "10.0.22621.0", "junk", ""},
	}
	dirs, err := d.includeDirs()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		filepath.Join("C:\\VS\\2022\\BuildTools", "VC", "Tools", "MSVC", "14.44.35207", "include"),
		filepath.Join("C:", "Kits", "10", "Include", "10.0.22621.0", "ucrt"),
		filepath.Join("C:", "Kits", "10", "Include", "10.0.22621.0", "shared"),
		filepath.Join("C:", "Kits", "10", "Include", "10.0.22621.0", "um"),
		filepath.Join("C:", "Kits", "10", "Include", "10.0.22621.0", "winrt"),
	}
	if strings.Join(dirs, "|") != strings.Join(want, "|") {
		t.Fatalf("include dirs\n got %v\nwant %v", dirs, want)
	}
}

func TestMSVCDiscoveryINCLUDEWins(t *testing.T) {
	d := msvcDiscovery{
		Include:    "C:\\vc\\include; C:\\sdk\\ucrt ;",
		VSWhereOut: "C:\\VS\\ignored",
	}
	dirs, err := d.includeDirs()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(dirs, "|") != "C:\\vc\\include|C:\\sdk\\ucrt" {
		t.Fatalf("INCLUDE dirs = %v", dirs)
	}
}

func TestMSVCDiscoveryFailsWithOneClearLine(t *testing.T) {
	for _, d := range []msvcDiscovery{
		{},
		{VSWhereOut: "C:\\VS"}, // no VC tools version, no SDK
		{VSWhereOut: "C:\\VS", VCToolsVersion: "14.44"}, // no SDK versions
	} {
		if _, err := d.includeDirs(); !errors.Is(err, errMSVCHeaders) {
			t.Fatalf("discovery %+v: err = %v, want errMSVCHeaders", d, err)
		}
	}
}

func TestNewestVersionName(t *testing.T) {
	got := newestVersionName([]string{"10.0.9.0", "10.0.22621.0", "10.0.19041.0"})
	if got != "10.0.22621.0" {
		t.Fatalf("newest = %q", got)
	}
	if newestVersionName(nil) != "" {
		t.Fatal("newest of nothing is not empty")
	}
}

func TestIsystemArgs(t *testing.T) {
	got := isystemArgs([]string{"a", "b"})
	if strings.Join(got, " ") != "-isystem a -isystem b" {
		t.Fatalf("isystem args = %v", got)
	}
}

func TestNativeHostIncludeArgsScope(t *testing.T) {
	// Off Windows there is nothing to add regardless of compiler.
	if args, err := nativeHostIncludeArgs("linux", "clang", nil); err != nil || args != nil {
		t.Fatalf("linux: %v %v", args, err)
	}
	// A non-clang compiler brings its own headers (mingw gcc).
	if args, err := nativeHostIncludeArgs("windows", `C:\mingw\bin\gcc.exe`, nil); err != nil || args != nil {
		t.Fatalf("gcc: %v %v", args, err)
	}
	// clang with a Developer-prompt INCLUDE uses it verbatim, no discovery.
	plan := &EnvironmentPlan{Env: []string{"INCLUDE=C:\\vc\\include"}}
	args, err := nativeHostIncludeArgs("windows", "clang", plan)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(args, " ") != "-isystem C:\\vc\\include" {
		t.Fatalf("clang + INCLUDE: %v", args)
	}
}

func TestClangFamily(t *testing.T) {
	for compiler, want := range map[string]bool{
		"clang": true, "clang++": true, "clang.exe": true,
		"cc": false, "gcc": false, "g++": false,
	} {
		if clangFamily(compiler) != want {
			t.Fatalf("clangFamily(%q) != %v", compiler, want)
		}
	}
}
