package polyglot

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestNativeDiagnosticsUseFenceSourcePosition(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang unavailable")
	}
	_, err = Prepare(context.Background(), []Block{{Language: "c", Filename: "program.bpp", Line: 17, Source: "int broken( { return 1; }\n"}}, map[string]Analyzer{"c": C{Command: clang}})
	if err == nil || !strings.Contains(err.Error(), "program.bpp:17") {
		t.Fatalf("diagnostic = %v", err)
	}
}

func TestNativeEnvironmentCompilerSelection(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "CMakeLists.txt"), []byte("cmake_minimum_required(VERSION 3.20)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bpp"), Language: "c", Environ: []string{"PATH=" + os.Getenv("PATH"), "BASHPP_CC=" + clang}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Language != "c" || plan.Executable == "" || plan.Root == "" || plan.Fingerprint == "" || len(plan.Manifests) != 1 {
		t.Fatalf("plan = %#v", plan)
	}
	if _, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bpp"), Language: "cpp", Environ: []string{"PATH="}}); err == nil || !strings.Contains(err.Error(), "C++ compiler unavailable") {
		t.Fatalf("missing compiler = %v", err)
	}
}

func TestCNativeArtifactCalls(t *testing.T) {
	clang, err := exec.LookPath("clang")
	if err != nil {
		t.Skip("clang unavailable")
	}
	runtime := C{Command: clang}
	plans, err := Prepare(context.Background(), []Block{{Language: "c", Source: `
#include <stdint.h>
#include <stdio.h>
#include <string.h>
int64_t add(int64_t a, int64_t b) { puts("c-out"); return a+b; }
const char* greet(const char* name) { static char out[80]; snprintf(out,sizeof out,"hello %s",name); return out; }
static int hidden(int x) { return x; }
`}}, map[string]Analyzer{"c": runtime})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != 1 || len(plans[0].Exports) != 2 || len(plans[0].Artifact) == 0 {
		t.Fatalf("plan = %#v", plans)
	}
	m := Start(plans[0], runtime)
	defer m.Close()
	got, err := m.Call(context.Background(), "add", int64(20), int64(22))
	if err != nil || got.Value != int64(42) || got.Stdout != "c-out\n" {
		t.Fatalf("add = %#v, %v", got, err)
	}
	got, err = m.Call(context.Background(), "greet", "world")
	if err != nil || got.Value != "hello world" {
		t.Fatalf("greet = %#v, %v", got, err)
	}
}

func TestCPPNativeArtifactCallsAndExceptions(t *testing.T) {
	clang, err := exec.LookPath("clang++")
	if err != nil {
		t.Skip("clang++ unavailable")
	}
	runtime := CPP{Command: clang}
	plans, err := Prepare(context.Background(), []Block{{Language: "cxx", Source: `
#include <stdexcept>
#include <string>
long long add(long long a, long long b) { return a+b; }
std::string greet(const std::string& name) { return "hello "+name; }
void fail() { throw std::runtime_error("native boom"); }
`}}, map[string]Analyzer{"cpp": runtime})
	if err != nil {
		t.Fatal(err)
	}
	m := Start(plans[0], runtime)
	defer m.Close()
	got, err := m.Call(context.Background(), "add", int64(19), int64(23))
	if err != nil || got.Value != int64(42) {
		t.Fatalf("add = %#v, %v", got, err)
	}
	got, err = m.Call(context.Background(), "greet", "world")
	if err != nil || got.Value != "hello world" {
		t.Fatalf("greet = %#v, %v", got, err)
	}
	if _, err := m.Call(context.Background(), "fail"); err == nil || !strings.Contains(err.Error(), "native boom") {
		t.Fatalf("fail = %v", err)
	}
}

func TestNativeIncludeRootsServeQuotedIncludesOnly(t *testing.T) {
	clang, err := exec.LookPath("clang++")
	if err != nil {
		t.Skip("clang++ unavailable")
	}
	// A project root carrying a file named like a standard header must not
	// shadow <version> (pulled in by <string>); it is still reachable through
	// a quoted include, which is the documented contract.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "version"), []byte("5.5.3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "coordinates.h"), []byte("#define PROJECT_MAJOR 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := DiscoverEnvironment(EnvironmentRequest{Source: filepath.Join(root, "program.bpp"), Language: "cpp", Environ: []string{"PATH=" + os.Getenv("PATH"), "BASHPP_CXX=" + clang}})
	if err != nil {
		t.Fatal(err)
	}
	if got := nativeIncludeArgs(&plan); len(got) < 2 || got[0] != "-iquote" {
		t.Fatalf("include args = %q", got)
	}
	runtime := CPP{Command: clang, Environment: &plan}
	plans, err := Prepare(context.Background(), []Block{{Language: "cxx", Filename: filepath.Join(root, "program.bpp"), Source: `
#include <string>
#include "coordinates.h"
std::string major() { return std::to_string(PROJECT_MAJOR); }
`}}, map[string]Analyzer{"cpp": runtime})
	if err != nil {
		t.Fatal(err)
	}
	m := Start(plans[0], runtime)
	defer m.Close()
	got, err := m.Call(context.Background(), "major")
	if err != nil || got.Value != "5" {
		t.Fatalf("major = %#v, %v", got, err)
	}
}

func TestNativeRejectsUnsupportedSurface(t *testing.T) {
	clang, err := exec.LookPath("clang++")
	if err != nil {
		t.Skip("clang++ unavailable")
	}
	for _, source := range []string{
		"struct Pair { int a; }; Pair pair() { return {1}; }\n",
		"int sum(int first, ...) { return first; }\n",
		"int value(int x) { return x; } double value(double x) { return x; }\n",
		"int state = 1; int value() { return state; }\n",
		"namespace hidden { int value() { return 1; } }\n",
	} {
		if _, _, err := (CPP{Command: clang}).AnalyzeArtifact(context.Background(), source); err == nil {
			t.Fatalf("accepted %q", source)
		}
	}
}
