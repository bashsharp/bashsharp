//go:build full

package interp_test

// Sprint: #374; Story: #1499; Story-ID: 6b3fc2bea3e5
//
// A seeded random sequence must match the native build of the same module,
// every run. The native default GODEBUG of a program comes from its module's
// go directive (a go1.23-or-older module keeps math/rand.Seed effective; the
// Go 1.24+ toolchain default makes it a no-op), so the differential below
// runs an unchanged seeded program inside a go1.22 module, builds it with the
// real go command, and requires two interpreter runs to print exactly the
// native sequence twice. The dependency worker is linked through the
// importcfg route, not cmd/go, so the toolchain's own defaults reach its
// runtime unless the original build's runtime.godebugDefault is replayed; a
// worker without it silently ignores rand.Seed and every run draws a fresh
// sequence.
import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// randSeedModule builds one original program inside a go1.22 module, runs the
// native build twice, and returns both outputs with the interpreter's own two
// outputs for the same source.
func randSeedModuleRun(t *testing.T, name, source string) (native [2]string, interpreted [2]string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module seeded\n\ngo 1.22\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name+".go")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	binary := filepath.Join(t.TempDir(), "oracle")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	build := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-p", "2", "-o", binary, path)
	build.Dir = dir
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("oracle build: %v %s", err, out)
	}
	for i := range native {
		out, err := exec.Command(binary).Output()
		if err != nil {
			t.Fatalf("oracle run: %v", err)
		}
		native[i] = string(out)
	}

	for i := range interpreted {
		program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
		if err != nil {
			t.Fatalf("gosource.Parse: %v", err)
		}
		var stdout, stderr bytes.Buffer
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir),
			interp.GoSourceModuleDir(dir), interp.StdIO(nil, &stdout, &stderr))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		if err := runner.Run(ctx, program.File); err != nil {
			var status interp.ExitStatus
			if !errors.As(err, &status) {
				t.Fatalf("Runner: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
		}
		interpreted[i] = stdout.String()
		if leftovers, _ := filepath.Glob(filepath.Join(dir, ".bashpp*")); len(leftovers) > 0 {
			t.Fatalf("bridge workspace leaked: %v", leftovers)
		}
	}
	return native, interpreted
}

func TestGoSourceRandSeedHonorsModuleGodebugDefaults(t *testing.T) {
	cases := []struct{ name, source string }{
		{"global_seed", `package main

import (
	"fmt"
	"math/rand"
)

func main() {
	rand.Seed(101)
	fmt.Println(rand.Intn(1000), rand.Intn(1000), rand.Intn(1000))
}
`},
		{"local_source", `package main

import (
	"fmt"
	"math/rand"
)

func main() {
	r := rand.New(rand.NewSource(202))
	fmt.Println(r.Intn(1000), r.Intn(1000), r.Intn(1000))
}
`},
		{"rand_v2_pcg", `package main

import (
	"fmt"
	"math/rand/v2"
)

func main() {
	r := rand.New(rand.NewPCG(77, 88))
	fmt.Println(r.IntN(1000), r.IntN(1000), r.IntN(1000))
}
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			native, interpreted := randSeedModuleRun(t, tc.name, tc.source)
			if native[0] != native[1] {
				t.Fatalf("native build is not deterministic under this module: %q vs %q", native[0], native[1])
			}
			if interpreted[0] != interpreted[1] {
				t.Fatalf("interpreted runs differ: %q vs %q", interpreted[0], interpreted[1])
			}
			if interpreted[0] != native[0] {
				t.Fatalf("Runner %q; native Go %q", interpreted[0], native[0])
			}
		})
	}
}
