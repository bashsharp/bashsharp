//go:build full

package interp

// Sprint: #319; Story: #1083; Story-ID: 91b27c7c0b52

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// A self-reexecuted Go-source program starts a new dependency bridge in every
// child process. The generated worker is large, but only its connection values
// differ between children. Compile that stable source once and link each child
// with its own values instead of compiling the worker on every replay.
func TestBashPPS319WorkerArchiveCacheAcrossChildren(t *testing.T) {
	cache := t.TempDir()
	t.Setenv(reexecPreparedCacheEnv, cache)
	t.Setenv(reexecInterpreterIDEnv, "s319-focused-interpreter")

	source := []byte("package main\nimport \"os\"\nvar bridgeAuthPath = \"\"\nfunc main() { data, err := os.ReadFile(bridgeAuthPath); if err != nil { panic(err) }; println(string(data)) }\n")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	compileLog := filepath.Join(t.TempDir(), "go-tool-compile.log")
	if runtime.GOOS != "windows" {
		wrapper := filepath.Join(t.TempDir(), "go-wrapper")
		script := "#!/bin/sh\nif [ \"$1\" = tool ] && [ \"$2\" = compile ]; then printf '%s\\n' \"$*\" >> " + shellQuote(compileLog) + "; fi\nexec " + shellQuote(goBinary) + " \"$@\"\n"
		if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		goBinary = wrapper
	}
	build := func(name, auth string, source []byte) string {
		t.Helper()
		work := t.TempDir()
		sourcePath := filepath.Join(work, "worker.go")
		if err := os.WriteFile(sourcePath, source, 0600); err != nil {
			t.Fatal(err)
		}
		binary := filepath.Join(work, name)
		if runtime.GOOS == "windows" {
			binary += ".exe"
		}
		authPath := filepath.Join(work, "bridge-auth")
		if err := os.WriteFile(authPath, []byte(auth), 0600); err != nil {
			t.Fatal(err)
		}
		if flags := bashPPWorkerBuildLDFlags(map[string]string{"bridgeAuthPath": authPath}); strings.Contains(flags, auth) {
			t.Fatalf("link flags exposed bridge auth: %s", flags)
		}
		if err := bashPPBuildWorkerImportcfg(context.Background(), goBinary, work, os.Environ(), "", work, sourcePath, binary, map[string]string{"bridgeAuthPath": authPath}, ""); err != nil {
			t.Fatal(err)
		}
		output, err := exec.Command(binary).CombinedOutput()
		if err != nil {
			t.Fatalf("run %s: %v: %s", name, err, output)
		}
		return strings.TrimSpace(string(output))
	}

	started := time.Now()
	if got := build("first", "first-session", source); got != "first-session" {
		t.Fatalf("first linked value = %q", got)
	}
	firstDuration := time.Since(started)
	first := s319WorkerArchives(t, cache)
	if len(first) != 1 {
		t.Fatalf("worker archives after first child = %v, want one", first)
	}
	if runtime.GOOS != "windows" {
		if got := s319CompileCount(t, compileLog); got != 1 {
			t.Fatalf("compile invocations after first child = %d, want one cache miss", got)
		}
	}
	info, err := os.Stat(first[0])
	if err != nil {
		t.Fatal(err)
	}
	firstModTime := info.ModTime()

	started = time.Now()
	if got := build("second", "second-session", source); got != "second-session" {
		t.Fatalf("second linked value = %q", got)
	}
	secondDuration := time.Since(started)
	second := s319WorkerArchives(t, cache)
	if len(second) != 1 || second[0] != first[0] {
		t.Fatalf("worker archives after identical child = %v, want reused %v", second, first)
	}
	info, err = os.Stat(second[0])
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstModTime) {
		t.Fatalf("cached worker archive was rewritten: first %v, second %v", firstModTime, info.ModTime())
	}
	if runtime.GOOS != "windows" {
		if got := s319CompileCount(t, compileLog); got != 1 {
			t.Fatalf("compile invocations after cached child = %d, want still one", got)
		}
		t.Logf("worker compile cache misses after cached child=%d", s319CompileCount(t, compileLog))
	}
	t.Logf("worker build first=%s cached-child=%s speedup=%.2fx", firstDuration, secondDuration, float64(firstDuration)/float64(secondDuration))

	changed := append(append([]byte(nil), source...), []byte("// distinct generated worker\n")...)
	if got := build("changed", "changed-session", changed); got != "changed-session" {
		t.Fatalf("changed linked value = %q", got)
	}
	if got := len(s319WorkerArchives(t, cache)); got != 2 {
		t.Fatalf("worker archives after source change = %d, want two", got)
	}
	if runtime.GOOS != "windows" {
		if got := s319CompileCount(t, compileLog); got != 2 {
			t.Fatalf("compile invocations after changed worker = %d, want two cache misses", got)
		}
		t.Logf("worker compile cache misses after changed source=%d", s319CompileCount(t, compileLog))
	}
}

func s319WorkerArchives(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var archives []string
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "worker-") && strings.HasSuffix(entry.Name(), ".a") {
			archives = append(archives, filepath.Join(dir, entry.Name()))
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			t.Fatalf("worker cache lock survived build: %s", entry.Name())
		}
	}
	return archives
}

func s319CompileCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Split(strings.TrimSpace(string(data)), "\n"))
}
