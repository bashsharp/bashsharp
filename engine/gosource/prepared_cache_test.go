//go:build full

package gosource_test

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
)

const (
	preparedCacheChildEnv     = "BASHPP_S281_PREPARED_CHILD"
	preparedCopiedCompilerEnv = "BASHPP_S319_COPIED_COMPILER"
)

func TestPreparedProgramCacheAcrossChildren(t *testing.T) {
	if os.Getenv(preparedCacheChildEnv) == "1" {
		dir := os.Getenv("BASHPP_S281_PREPARED_SOURCE")
		var sources []gosource.Source
		for _, name := range []string{"doc.go", "main.go", "script_test.go"} {
			path := filepath.Join(dir, name)
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sources = append(sources, gosource.Source{Name: path, Data: data})
		}
		started := time.Now()
		options := gosource.Options{RunMain: true, ImportPath: "cmd/compile"}
		if os.Getenv(preparedCopiedCompilerEnv) == "1" {
			options.Importer = lower.NewModuleImporter(dir)
		}
		program, err := gosource.Load(sources, options)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Printf("PREPARED %d %s %s %d\n", time.Since(started).Nanoseconds(), program.Package, program.Main, len(program.File.Stmts))
		return
	}

	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	sourceDir := filepath.Join(dir, "compiler")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	sdkDir := filepath.Join(runtime.GOROOT(), "src", "cmd", "compile")
	for _, name := range []string{"doc.go", "main.go", "script_test.go"} {
		data, err := os.ReadFile(filepath.Join(sdkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	firstDuration, firstOutput := runPreparedCacheChild(t, cache, sourceDir)
	secondDuration, secondOutput := runPreparedCacheChild(t, cache, sourceDir)
	if firstOutput != secondOutput {
		t.Fatalf("prepared child outputs differ:\nfirst:  %s\nsecond: %s", firstOutput, secondOutput)
	}
	if secondDuration*5 > firstDuration {
		t.Fatalf("second child preparation %s is not at least 5x faster than first %s", secondDuration, firstDuration)
	}
	t.Logf("first child %s; second child %s; speedup %.1fx", firstDuration, secondDuration, float64(firstDuration)/float64(secondDuration))
	if got := preparedCacheEntries(t, cache); got != 1 {
		t.Fatalf("cache entries after repeated source = %d, want 1", got)
	}

	// A comment changes no behavior, but it changes an authenticated source
	// byte. The next child must prepare and publish a distinct entry.
	docPath := filepath.Join(sourceDir, "doc.go")
	doc, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docPath, append(doc, []byte("\n// changed byte\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	_, changedOutput := runPreparedCacheChild(t, cache, sourceDir)
	if changedOutput != firstOutput {
		t.Fatalf("source-comment change altered output: before %q after %q", firstOutput, changedOutput)
	}
	if got := preparedCacheEntries(t, cache); got != 2 {
		t.Fatalf("cache entries after source change = %d, want 2", got)
	}

	t.Run("single flight", func(t *testing.T) {
		concurrentCache := filepath.Join(dir, "concurrent-cache")
		const children = 4
		outputs := make([]string, children)
		var group sync.WaitGroup
		for i := range children {
			group.Add(1)
			go func() {
				defer group.Done()
				_, outputs[i] = runPreparedCacheChild(t, concurrentCache, sourceDir)
			}()
		}
		group.Wait()
		for i := 1; i < children; i++ {
			if outputs[i] != outputs[0] {
				t.Fatalf("concurrent output %d = %q, want %q", i, outputs[i], outputs[0])
			}
		}
		if got := preparedCacheEntries(t, concurrentCache); got != 1 {
			t.Fatalf("concurrent cache entries = %d, want one atomic publication", got)
		}
	})
}

// TestPreparedProgramCacheAcrossCopiedCompilerRoots models cmd/compile's
// TestScript setup. Each script gets a private test GOROOT and copies the same
// interpreted test binary over its compile tool. The launcher pins BASHPP_GO
// to the authenticated SDK which prepares the program, so the program-visible
// test GOROOT must not give identical compiler sources a fresh prepared entry.
// Sprint: #319; Story: #1083; Story-ID: 91b27c7c0b52.
func TestPreparedProgramCacheAcrossCopiedCompilerRoots(t *testing.T) {
	dir := t.TempDir()
	cache := filepath.Join(dir, "cache")
	sourceDir := filepath.Join(dir, "compiler")
	if err := os.Mkdir(sourceDir, 0700); err != nil {
		t.Fatal(err)
	}
	sdkDir := filepath.Join(runtime.GOROOT(), "src", "cmd", "compile")
	for _, name := range []string{"doc.go", "main.go", "script_test.go"} {
		data, err := os.ReadFile(filepath.Join(sdkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sourceDir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	firstRoot := filepath.Join(dir, "TestScript-one", "testgoroot")
	secondRoot := filepath.Join(dir, "TestScript-two", "testgoroot")
	for _, root := range []string{firstRoot, secondRoot} {
		if err := os.MkdirAll(filepath.Dir(root), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(runtime.GOROOT(), root); err != nil {
			t.Fatal(err)
		}
	}
	firstDuration, firstOutput := runPreparedCacheChildEnv(t, cache, sourceDir,
		preparedCopiedCompilerEnv+"=1", "BASHPP_GO="+goBinary, "GOROOT="+firstRoot)
	secondDuration, secondOutput := runPreparedCacheChildEnv(t, cache, sourceDir,
		preparedCopiedCompilerEnv+"=1", "BASHPP_GO="+goBinary, "GOROOT="+secondRoot)
	if firstOutput != secondOutput {
		t.Fatalf("copied compiler outputs differ:\nfirst:  %s\nsecond: %s", firstOutput, secondOutput)
	}
	if got := preparedCacheEntries(t, cache); got != 1 {
		t.Fatalf("cache entries after copied compiler roots = %d, want one; first=%s second=%s", got, firstDuration, secondDuration)
	}

	// Without a pinned SDK, the ambient GOROOT remains part of the cache
	// identity. A separate test root may otherwise supply a different source
	// tree for imports even when the top-level compiler files are identical.
	unpinnedCache := filepath.Join(dir, "unpinned-cache")
	_, _ = runPreparedCacheChildEnv(t, unpinnedCache, sourceDir,
		"BASHPP_GO=", "GOROOT="+firstRoot)
	_, _ = runPreparedCacheChildEnv(t, unpinnedCache, sourceDir,
		"BASHPP_GO=", "GOROOT="+secondRoot)
	if got := preparedCacheEntries(t, unpinnedCache); got != 2 {
		t.Fatalf("cache entries without a pinned SDK = %d, want two distinct GOROOT identities", got)
	}
}

func runPreparedCacheChild(t *testing.T, cache, source string) (time.Duration, string) {
	return runPreparedCacheChildEnv(t, cache, source)
}

func runPreparedCacheChildEnv(t *testing.T, cache, source string, extraEnv ...string) (time.Duration, string) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(self, "-test.run=^TestPreparedProgramCacheAcrossChildren$")
	command.Env = append(os.Environ(),
		preparedCacheChildEnv+"=1",
		"BASHPP_S281_PREPARED_SOURCE="+source,
		"BASHPP_REEXEC_PREPARED_CACHE="+cache,
		"BASHPP_REEXEC_INTERPRETER_ID=focused-test-binary",
	)
	command.Env = append(command.Env, extraEnv...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("prepared child: %v: %s", err, output)
	}
	line := []byte(nil)
	for _, candidate := range bytes.Split(output, []byte{'\n'}) {
		if bytes.HasPrefix(candidate, []byte("PREPARED ")) {
			line = candidate
			break
		}
	}
	fields := strings.Fields(string(line))
	if len(fields) != 5 || fields[0] != "PREPARED" {
		t.Fatalf("prepared child output = %q", output)
	}
	nanos, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return time.Duration(nanos), strings.Join(fields[2:], " ")
}

func preparedCacheEntries(t *testing.T, cache string) int {
	t.Helper()
	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			count++
		}
		if strings.HasSuffix(entry.Name(), ".lock") {
			t.Fatalf("cache lock left behind: %s", entry.Name())
		}
	}
	return count
}
