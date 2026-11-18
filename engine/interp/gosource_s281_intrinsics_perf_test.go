//go:build full

package interp

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// The Go 1.27.1 ssagen TestIntrinsics workload builds and scans about 1,300
// map entries keyed by {arch *sys.Arch, pkg, fn}. Its values are local
// function closures. This outside-corpus reproduction keeps the same costly
// ingredients: pointers obtained from a native slice, struct keys, a
// comma-ok probe before every store, local function values, and repeated
// lookups. It is intentionally standalone so a regression can be profiled in
// seconds instead of consuming a package-root timeout.

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func s281IntrinsicMapSource(entries int) string {
	var src strings.Builder
	src.WriteString(`package main
import ("fmt"; "unicode")
type intrinsicKey struct {
	arch *unicode.RangeTable
	pkg  string
	fn   string
}
type intrinsicBuilder func() int
var names = []string{
`)
	for i := 0; i < entries; i++ {
		src.WriteString(strconv.Quote("fn" + strconv.Itoa(i)))
		src.WriteString(",\n")
	}
	src.WriteString(`}
func main() {
	m := map[intrinsicKey]intrinsicBuilder{}
	for i, name := range names {
		arch := unicode.GraphicRanges[i%len(unicode.GraphicRanges)]
		key := intrinsicKey{arch, "runtime", name}
		if _, found := m[key]; found { panic("duplicate intrinsic") }
		m[key] = func() int { return i + len(name) }
	}
	hits := 0
	for i, name := range names {
		arch := unicode.GraphicRanges[i%len(unicode.GraphicRanges)]
		if b, found := m[intrinsicKey{arch, "runtime", name}]; found {
			hits += b()
		}
	}
	fmt.Println(len(m), hits)
}
`)
	return src.String()
}

func s281IntrinsicExpectedOutput(entries int) string {
	hits := entries * (entries - 1) / 2
	for i := 0; i < entries; i++ {
		hits += len("fn" + strconv.Itoa(i))
	}
	return strconv.Itoa(entries) + " " + strconv.Itoa(hits)
}

func runS281IntrinsicMap(tb testing.TB, entries int) (load, run time.Duration, output string) {
	tb.Helper()
	start := time.Now()
	program, err := gosource.Parse(strings.NewReader(s281IntrinsicMapSource(entries)), "intrinsics.go", gosource.Options{RunMain: true})
	load = time.Since(start)
	if err != nil {
		tb.Fatalf("parse %d entries: %v", entries, err)
	}
	var stdout, stderr bytes.Buffer
	runner, err := New(Lang(syntax.LangBashPP), Dir(tb.TempDir()), StdIO(nil, &stdout, &stderr))
	if err != nil {
		tb.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	start = time.Now()
	err = runner.Run(ctx, program.File)
	run = time.Since(start)
	if err != nil || stderr.Len() != 0 {
		tb.Fatalf("run %d entries: err=%v stderr=%q", entries, err, stderr.String())
	}
	return load, run, strings.TrimSpace(stdout.String())
}

func TestS281IntrinsicMapWorkScalesLinearly(t *testing.T) {
	const small, large = 64, 1024
	smallLoad, smallRun, smallOut := runS281IntrinsicMap(t, small)
	largeLoad, largeRun, largeOut := runS281IntrinsicMap(t, large)
	if smallOut != s281IntrinsicExpectedOutput(small) || largeOut != s281IntrinsicExpectedOutput(large) {
		t.Fatalf("wrong result: small=%q large=%q", smallOut, largeOut)
	}
	t.Logf("small: load=%v run=%v; large: load=%v run=%v; run ratio=%.2f", smallLoad, smallRun, largeLoad, largeRun, float64(largeRun)/float64(smallRun))
	// The useful work grows 16x. Leave headroom for fixed bridge startup and
	// noisy builders, but reject a quadratic map-key/storage path.
	if ratio := float64(largeRun) / float64(smallRun); ratio > 40 {
		t.Fatalf("intrinsic-shaped map work grows superlinearly: %.2fx", ratio)
	}
}

func BenchmarkS281IntrinsicMap(b *testing.B) {
	for b.Loop() {
		_, _, output := runS281IntrinsicMap(b, 1024)
		if output != s281IntrinsicExpectedOutput(1024) {
			b.Fatalf("wrong result: %q", output)
		}
	}
}

func TestS281BridgeMetadataCacheInvalidation(t *testing.T) {
	parse := func(source string) *syntax.File {
		t.Helper()
		program, err := gosource.Parse(strings.NewReader(source), "metadata.go", gosource.Options{RunMain: true})
		if err != nil {
			t.Fatal(err)
		}
		return program.File
	}
	firstFile := parse(`package main
import "strings"
func main() { _ = strings.TrimSpace(" x ") }
`)
	runner, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	runner.Reset()
	runner.bashPPGoSourceFile = firstFile
	runner.bashPPImports = map[string]string{"strings": "strings"}
	first := runner.bashPPBridgeMetadata()
	if again := runner.bashPPBridgeMetadata(); again != first {
		t.Fatal("unchanged source metadata was rebuilt")
	}
	if len(first.selectors) != 1 || first.selectors[0] != "strings.TrimSpace" {
		t.Fatalf("selectors = %v", first.selectors)
	}

	// Import registration participates in generic type identity and selector
	// resolution, so even a binding change on the same file invalidates.
	runner.bashPPImports["strings"] = "bytes"
	importsChanged := runner.bashPPBridgeMetadata()
	if importsChanged == first {
		t.Fatal("import replacement reused stale metadata")
	}

	secondFile := parse(`package main
import "fmt"
func main() { fmt.Println("ok") }
`)
	runner.bashPPGoSourceFile = secondFile
	runner.bashPPImports = map[string]string{"fmt": "fmt"}
	second := runner.bashPPBridgeMetadata()
	if second == importsChanged || len(second.selectors) != 1 || second.selectors[0] != "fmt.Println" {
		t.Fatalf("source replacement metadata = %#v", second)
	}

	// A subshell/callback Runner copies bashPPTools by value. It may share the
	// immutable hit, but an import mutation replaces only the child's pointer.
	child := runner.Subshell()
	child.bashPPImports = map[string]string{"fmt": "log"}
	childCache := child.bashPPBridgeMetadata()
	if childCache == second {
		t.Fatal("child import mutation reused parent metadata")
	}
	if runner.bashPPTools.bridgeMetadata != second {
		t.Fatal("child invalidation changed parent metadata")
	}

	resetRunner, err := New(Lang(syntax.LangBashPP))
	if err != nil {
		t.Fatal(err)
	}
	resetRunner.bashPPGoSourceFile = firstFile
	resetRunner.bashPPImports = map[string]string{"strings": "strings"}
	beforeReset := resetRunner.bashPPBridgeMetadata()
	resetRunner.Reset()
	if afterReset := resetRunner.bashPPBridgeMetadata(); afterReset == beforeReset || afterReset.file != nil {
		t.Fatal("Reset reused prior source metadata")
	}
}

func TestS281ConnectedEvalRequestReusesCachedEnvs(t *testing.T) {
	hasEnv := func(env []string, entry string) bool {
		for _, got := range env {
			if got == entry {
				return true
			}
		}
		return false
	}

	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	runner := &Runner{
		bashPPGoSource: true,
		bashPPTools: bashPPToolchain{
			bridge:         &bashPPNativeSession{conn: left},
			goBinary:       "go",
			goRoot:         "/runtime/root",
			goVersion:      bashPPReviewedGoVersion,
			buildGoBinary:  "go",
			buildGoRoot:    "/build/root",
			buildGoVersion: bashPPReviewedGoVersion,
			requestEnv:     []string{"PATH=/bin", "GOROOT=/runtime/root", "GOTOOLCHAIN=" + bashPPReviewedGoVersion},
			buildEnv:       []string{"PATH=/bin", "GOROOT=/build/root", "GOTOOLCHAIN=" + bashPPReviewedGoVersion},
			runtimeEnv:     []string{"PATH=/bin", "GOROOT=/runtime/root", "GOTOOLCHAIN=" + bashPPReviewedGoVersion},
		},
		bashPPImports: map[string]string{},
	}
	reqEnv, buildEnv, runtimeEnv := runner.bashPPTools.requestEnv, runner.bashPPTools.buildEnv, runner.bashPPTools.runtimeEnv
	request, err := runner.bashPPEvalRequest()
	if err != nil {
		t.Fatal(err)
	}
	if &request.Env[0] != &reqEnv[0] || &request.BuildEnv[0] != &buildEnv[0] || &request.RuntimeEnv[0] != &runtimeEnv[0] {
		t.Fatal("connected request rebuilt cached environments")
	}
	if &runner.bashPPTools.requestEnv[0] != &reqEnv[0] || &runner.bashPPTools.buildEnv[0] != &buildEnv[0] || &runner.bashPPTools.runtimeEnv[0] != &runtimeEnv[0] {
		t.Fatal("connected request replaced cached environments")
	}

	newRequestRunner := func(path, goRoot, buildRoot string) *Runner {
		env := expand.ListEnviron("PATH=" + path)
		return &Runner{
			Env:            env,
			writeEnv:       newOverlayEnviron(env, false),
			bashPPGoSource: true,
			bashPPTools: bashPPToolchain{
				goBinary:       "go",
				goRoot:         goRoot,
				goVersion:      bashPPReviewedGoVersion,
				buildGoBinary:  "go",
				buildGoRoot:    buildRoot,
				buildGoVersion: bashPPReviewedGoVersion,
			},
		}
	}
	rebuildRunner := newRequestRunner("/bin", "/runtime/new", "/build/new")
	rebuilt, err := rebuildRunner.bashPPEvalRequest()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnv(rebuilt.Env, "GOROOT=/runtime/new") || !hasEnv(rebuilt.RuntimeEnv, "GOROOT=/runtime/new") || !hasEnv(rebuilt.BuildEnv, "GOROOT=/build/new") {
		t.Fatalf("disconnected request reused stale environment: env=%v build=%v runtime=%v", rebuilt.Env, rebuilt.BuildEnv, rebuilt.RuntimeEnv)
	}
	if len(rebuilt.Env) == 0 || len(rebuilt.BuildEnv) == 0 || &rebuilt.Env[0] == &rebuilt.BuildEnv[0] {
		t.Fatal("build env aliases request env storage")
	}

	otherRunner := newRequestRunner("/usr/bin", "/runtime/other", "/build/other")
	other, err := otherRunner.bashPPEvalRequest()
	if err != nil {
		t.Fatal(err)
	}
	if !hasEnv(other.Env, "PATH=/usr/bin") || !hasEnv(other.Env, "GOROOT=/runtime/other") || !hasEnv(other.BuildEnv, "GOROOT=/build/other") {
		t.Fatalf("second runner has wrong environment: env=%v build=%v runtime=%v", other.Env, other.BuildEnv, other.RuntimeEnv)
	}
	if hasEnv(rebuildRunner.bashPPTools.requestEnv, "GOROOT=/runtime/other") || hasEnv(otherRunner.bashPPTools.requestEnv, "GOROOT=/runtime/new") {
		t.Fatal("runner environment caches crossed content")
	}
	if &rebuildRunner.bashPPTools.requestEnv[0] == &otherRunner.bashPPTools.requestEnv[0] || &rebuildRunner.bashPPTools.buildEnv[0] == &otherRunner.bashPPTools.buildEnv[0] {
		t.Fatal("runner environment caches share backing storage")
	}

	rebuildRunner.closeGoSourceBridge()
	if rebuildRunner.bashPPTools.requestEnv != nil || rebuildRunner.bashPPTools.buildEnv != nil || rebuildRunner.bashPPTools.runtimeEnv != nil {
		t.Fatal("close did not clear cached environments")
	}
}
