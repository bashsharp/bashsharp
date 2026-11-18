//go:build full

package interp

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543
//
// The per-program native request plan (bashpp_s281_request_plan.go) derives the
// program-dependent part of a native eval request once and reuses it across
// every later request. Reuse is only sound while the derivation's inputs are
// unchanged, and the request path runs at arbitrary points of execution: inside
// a frame that declared a function-local type of a reused name (which shadows
// the runtime type registry), after an instantiation of a generic was reached,
// with methods registered on the runtime method table.
//
// This is the staleness oracle for that reuse. It compares what each request
// was actually handed against a derivation made from scratch at that exact
// point of execution: the session drift fingerprints the helper is generated
// from, the descriptor counts, and the local type plan's lookups. A reuse key
// that misses an input the derivation reads shows up here as a fingerprint the
// live session would have refused, or as a lookup answering for a namespace the
// program no longer has.

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"maps"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// s281RequestPlanProgram makes a native fmt call at each of the three points
// the reuse key has to survive: at top level before any frame ran, inside two
// functions each declaring a function-local type of the same reused name whose
// method set is a promoted original method, and after generic instantiations
// the program reaches. decls pads the program with unrelated named types so the
// scan instrument can be read against declaration count.
func s281RequestPlanProgram(decls, calls int) string {
	var sb strings.Builder
	sb.WriteString(`package main

import "fmt"

type Top struct{ N int }

func (t Top) String() string { return fmt.Sprintf("top(%d)", t.N) }

type Box[T any] struct{ V T }

func (b Box[T]) String() string { return fmt.Sprint("box[", b.V, "]") }

func scopedA() string {
	type T struct{ Top }
	return fmt.Sprint(T{Top{7}})
}

func scopedB() string {
	type T struct {
		N int
		Top
	}
	return fmt.Sprint(T{8, Top{9}}.Top)
}

func boxed[T any](v T) string {
	return fmt.Sprint(Box[T]{v})
}
`)
	for i := range decls {
		fmt.Fprintf(&sb, "\ntype pad%d struct{ A, B int }\n", i)
	}
	fmt.Fprintf(&sb, `
func main() {
	fmt.Println(fmt.Sprint(Top{1}))
	fmt.Println(scopedA())
	fmt.Println(scopedB())
	fmt.Println(boxed(2))
	fmt.Println(boxed("x"))
	fmt.Println(fmt.Sprint(Top{3}))
	total := 0
	for i := 0; i < %d; i++ {
		total += len(fmt.Sprint(Top{i}))
	}
	fmt.Println(total > 0)
}
`, calls)
	return sb.String()
}

const s281RequestPlanWant = "top(1)\ntop(7)\ntop(9)\nbox[2]\nbox[x]\ntop(3)\ntrue"

// s281FreshRequestShape derives the request shape from scratch against this
// runner's state right now: every memo the derivation reads is dropped for the
// derivation and restored afterwards, so the result is what the request path
// would have produced with no reuse at all.
func s281FreshRequestShape(r *Runner) *bashPPNativeRequestPlan {
	savedTrace := bashPPRequestPlanTrace
	savedPlan, savedLocals := r.bashPPTools.requestPlan, r.bashPPTools.localTypes
	savedDecls, savedMeta := r.bashPPTools.localTypeDecls, r.bashPPTools.bridgeMetadata
	savedInstances := r.bashPPTools.instantiations
	bashPPRequestPlanTrace = nil
	r.bashPPTools.requestPlan, r.bashPPTools.localTypes = nil, nil
	r.bashPPTools.localTypeDecls, r.bashPPTools.bridgeMetadata = nil, nil
	r.bashPPTools.instantiations = nil
	fresh := r.bashPPNativeRequestShape()
	bashPPRequestPlanTrace = savedTrace
	r.bashPPTools.requestPlan, r.bashPPTools.localTypes = savedPlan, savedLocals
	r.bashPPTools.localTypeDecls, r.bashPPTools.bridgeMetadata = savedDecls, savedMeta
	r.bashPPTools.instantiations = savedInstances
	return fresh
}

// s281PlanShape renders everything a request reads out of its plan, so a
// difference names the field that went stale rather than only reporting one.
func s281PlanShape(plan *bashPPNativeRequestPlan) map[string]string {
	id := plan.identity
	shape := map[string]string{
		"identity.imports":    id.imports,
		"identity.locals":     id.locals,
		"identity.embeds":     id.embeds,
		"identity.companions": id.companions,
		"identity.cgo":        id.cgo,
		"identity.instances":  id.instances,
		"identity.generics":   id.generics,
		"selectors":           strings.Join(plan.selectors, ","),
		"genericTypes":        strings.Join(plan.genericTypes, ","),
		"localTypes":          fmt.Sprint(len(plan.localTypes)),
		"sourceFile":          plan.sourceFile,
		"sourceDir":           plan.sourceDir,
		"err":                 fmt.Sprint(plan.err),
	}
	keys := make([]string, 0, len(plan.instances))
	for _, inst := range plan.instances {
		keys = append(keys, inst.Key+"="+strings.Join(inst.Args, "+"))
	}
	sort.Strings(keys)
	shape["instances"] = strings.Join(keys, ",")
	if lp := plan.localPlan; lp != nil {
		names := make([]string, 0, len(lp.callbackNames))
		for name := range lp.callbackNames {
			names = append(names, name)
		}
		sort.Strings(names)
		shape["localPlan.callbackNames"] = strings.Join(names, ",")
		shape["localPlan.methodsMirrored"] = fmt.Sprint(lp.methodsMirrored)
		shape["localPlan.fmtFormatting"] = fmt.Sprint(lp.fmtFormatting)
		shape["localPlan.identity"] = lp.identity
		shape["localPlan.byName"] = fmt.Sprint(len(lp.byName))
		shape["localPlan.byTransport"] = fmt.Sprint(len(lp.byTransport))
	}
	return shape
}

type s281PlanDrift struct {
	field      string
	reused     string
	fresh      string
	localTypes int
}

// runS281RequestPlanProgram runs one program and reports the total descriptor
// entries the request path scanned and the number of plan derivations and reuses
// it made. With the oracle armed it additionally reports every reuse whose plan
// disagreed with a derivation made at that exact point of execution — which
// costs a derivation of its own per reuse, so the scan instrument is only
// meaningful with the oracle off.
func runS281RequestPlanProgram(t *testing.T, source string, oracle bool) (stdout string, drift []s281PlanDrift, scans, derivations, reuses int64) {
	t.Helper()
	var mu sync.Mutex
	var out, errout bytes.Buffer
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	bashPPRequestPlanTrace = func(owner *Runner, plan *bashPPNativeRequestPlan, reused bool) {
		mu.Lock()
		defer mu.Unlock()
		if !reused {
			derivations++
			return
		}
		reuses++
		if !oracle {
			return
		}
		fresh := s281FreshRequestShape(owner)
		have, want := s281PlanShape(plan), s281PlanShape(fresh)
		for _, field := range sortedKeys(want) {
			if have[field] != want[field] {
				drift = append(drift, s281PlanDrift{field: field, reused: have[field], fresh: want[field], localTypes: len(plan.localTypes)})
			}
		}
	}
	defer func() { bashPPRequestPlanTrace = nil }()
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	runErr := r.Run(ctx, p.File)
	mu.Lock()
	defer mu.Unlock()
	if runErr != nil || errout.Len() > 0 {
		t.Fatalf("run: err=%v stderr=%s", runErr, errout.String())
	}
	if r.bashPPTools.requestScans != nil {
		scans = r.bashPPTools.requestScans.Load()
	}
	return strings.TrimSpace(out.String()), drift, scans, derivations, reuses
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestS281RequestPlanNeverStale is the correctness gate on plan reuse: every
// reused plan must describe the same program a derivation made at that moment
// describes. A reuse key that misses a runtime-varying input fails here with
// the fingerprint the live helper session would have refused.
func TestS281RequestPlanNeverStale(t *testing.T) {
	stdout, drift, scans, derivations, reuses := runS281RequestPlanProgram(t, s281RequestPlanProgram(4, 1), true)
	if stdout != s281RequestPlanWant {
		t.Fatalf("stdout =\n%s\nwant\n%s", stdout, s281RequestPlanWant)
	}
	t.Logf("plan derivations=%d reuses=%d descriptor scans=%d", derivations, reuses, scans)
	if len(drift) > 0 {
		for _, d := range drift {
			t.Errorf("reused plan stale: %s\n reused=%q\n fresh =%q (localTypes=%d)", d.field, d.reused, d.fresh, d.localTypes)
		}
		t.FailNow()
	}
	if derivations == 0 {
		t.Fatal("no plan was derived: the oracle never observed the request path")
	}
}

// s281PlanRunner is a runner loaded with one Go-source program, ready for the
// request path without running it: the plan is a function of the loaded program
// and the runner's state, so every input in its reuse key can be moved
// deliberately and the derivation asked for again.
func s281PlanRunner(t *testing.T, source string) (*Runner, *syntax.File) {
	t.Helper()
	r, err := New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, io.Discard))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.closeDirFile() })
	p, err := gosource.Parse(strings.NewReader(source), filepath.Join(r.Dir, "original.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	r.Reset()
	r.bashPPGoSource = true
	r.bashPPGoSourceFile = p.File
	r.bashPPImports = map[string]string{"fmt": "fmt"}
	return r, p.File
}

// s281AssertPlanCoherent is the invariant plan reuse owes the request path: the
// descriptor sets a request is handed must be the ones the runner's own
// accessors return at that moment, because the rest of the request path — the
// transport spellings, [Runner.bashPPScopedLocalTypeName], the helper's own
// registration — reads them from the runner directly while the same request is
// assembled. A reuse key that misses an input serves the predecessor's
// descriptors to a request the runner now describes differently.
func s281AssertPlanCoherent(t *testing.T, r *Runner, what string) *bashPPNativeRequestPlan {
	t.Helper()
	plan := r.bashPPNativeRequestShape()
	if live := r.bashPPLocalTypeDescriptors(); !sameLocalTypeSet(plan.localTypes, live) {
		t.Errorf("%s: plan carries %d local type descriptors, runner now has %d (%s)",
			what, len(plan.localTypes), len(live), "stale plan: local type namespace not in the reuse key")
	}
	if live := r.bashPPGenericBridgeTypes(); strings.Join(plan.genericTypes, ",") != strings.Join(live, ",") {
		t.Errorf("%s: plan generic bridge types %q, runner now has %q", what, plan.genericTypes, live)
	}
	if live := r.bashPPReferencedSelectors(); strings.Join(plan.selectors, ",") != strings.Join(live, ",") {
		t.Errorf("%s: plan selectors %q, runner now has %q", what, plan.selectors, live)
	}
	if live := r.bashPPImportedInstances(); len(plan.instances) != len(live) {
		t.Errorf("%s: plan carries %d imported instantiations, runner now has %d", what, len(plan.instances), len(live))
	}
	if plan.identity.locals != bashPPLocalTypeIdentity(plan.localTypes) {
		t.Errorf("%s: session local-type fingerprint does not describe the descriptors the request carries", what)
	}
	if plan.localPlan != nil && !sameLocalTypeSet(plan.localPlan.types, plan.localTypes) {
		t.Errorf("%s: local type lookups were derived from a different namespace than the request carries", what)
	}
	return plan
}

// TestS281RequestPlanKeyCoversEveryInput moves each input the derivation reads
// and requires the plan to follow it. The descriptor sets are lexical, but they
// are read through per-builder memos on the toolchain, and the plan is only
// valid while those memos are the live ones — a memo rebuilt under its own key,
// or a runner that stops being a Go-source runner, must not be served the
// predecessor's descriptors.
func TestS281RequestPlanKeyCoversEveryInput(t *testing.T) {
	r, file := s281PlanRunner(t, s281RequestPlanProgram(4, 1))
	first := s281AssertPlanCoherent(t, r, "first derivation")
	if len(first.localTypes) == 0 {
		t.Fatal("program derived no local type descriptors: the test proves nothing")
	}
	if again := r.bashPPNativeRequestShape(); again != first {
		t.Fatal("unchanged inputs re-derived the plan: the reuse the story rests on is gone")
	}

	// Each case moves one input, requires the plan to be re-derived rather than
	// reused, and requires the re-derivation to describe the runner's state now.
	cases := []struct {
		name    string
		mutate  func() func()
		rebuild bool
	}{{
		name: "imports", mutate: func() func() {
			saved := r.bashPPImports
			r.bashPPImports = map[string]string{"fmt": "fmt", "sort": "sort"}
			return func() { r.bashPPImports = saved }
		}, rebuild: true,
	}, {
		name: "dir", mutate: func() func() {
			saved := r.Dir
			r.Dir = filepath.Join(saved, "nested")
			return func() { r.Dir = saved }
		}, rebuild: true,
	}, {
		name: "moduleDir", mutate: func() func() {
			saved := r.bashPPTools.moduleDir
			r.bashPPTools.moduleDir = filepath.Join(r.Dir, "module")
			return func() { r.bashPPTools.moduleDir = saved }
		}, rebuild: true,
	}, {
		name: "goSource", mutate: func() func() {
			// A nested Run of a plain shell File leaves the runner with the
			// same loaded program and no Go-source semantics; the descriptor
			// builders answer nil for it, so a plan reused across the flip
			// would register a namespace the program does not have.
			r.bashPPGoSource = false
			return func() { r.bashPPGoSource = true }
		}, rebuild: true,
	}, {
		name: "localTypes memo", mutate: func() func() {
			saved := r.bashPPTools.localTypes
			r.bashPPTools.localTypes = &bashPPLocalTypeCache{
				file: file, imports: maps.Clone(r.bashPPImports),
				types: saved.types[:len(saved.types)-1], scoped: saved.scoped, nest: saved.nest,
			}
			return func() { r.bashPPTools.localTypes = saved }
		}, rebuild: true,
	}, {
		name: "bridge metadata memo", mutate: func() func() {
			saved := r.bashPPTools.bridgeMetadata
			r.bashPPTools.bridgeMetadata = &bashPPBridgeMetadataCache{
				file: file, imports: maps.Clone(r.bashPPImports),
				genericTypes: nil, selectors: []string{"fmt.Sprint"},
			}
			return func() { r.bashPPTools.bridgeMetadata = saved }
		}, rebuild: true,
	}, {
		name: "instantiation memo", mutate: func() func() {
			saved := r.bashPPTools.instantiations
			r.bashPPTools.instantiations = &bashPPInstantiationIndex{file: file}
			return func() { r.bashPPTools.instantiations = saved }
		}, rebuild: true,
	}, {
		name: "declaration index memo", mutate: func() func() {
			saved := r.bashPPTools.localTypeDecls
			r.bashPPTools.localTypeDecls = nil
			return func() { r.bashPPTools.localTypeDecls = saved }
		}, rebuild: true,
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			restore := tc.mutate()
			defer restore()
			moved := s281AssertPlanCoherent(t, r, tc.name)
			if tc.rebuild && moved == first {
				t.Errorf("%s changed but the plan was reused: the input is missing from the reuse key", tc.name)
			}
		})
		// Restoring the input must be enough to describe the original program
		// again; a plan that survives the round trip describes the wrong one.
		s281AssertPlanCoherent(t, r, "after restoring "+tc.name)
	}
}

// TestS281RequestPlanScansFlatPerCall is the speedup gate. The instrument counts
// the local type descriptor entries the request path examined, so a per-request
// derivation makes it grow with the number of native calls and a per-program one
// does not. Two runs of the same program differing only in how many native calls
// it makes must scan the same number of entries.
func TestS281RequestPlanScansFlatPerCall(t *testing.T) {
	const decls = 256
	const few, many = 1, 64
	_, _, scansFew, derivationsFew, reusesFew := runS281RequestPlanProgram(t, s281RequestPlanProgram(decls, few), false)
	_, _, scansMany, derivationsMany, reusesMany := runS281RequestPlanProgram(t, s281RequestPlanProgram(decls, many), false)
	t.Logf("decls=%d | calls=%d requests=%d derivations=%d scans=%d | calls=%d requests=%d derivations=%d scans=%d",
		decls, few, derivationsFew+reusesFew, derivationsFew, scansFew,
		many, derivationsMany+reusesMany, derivationsMany, scansMany)
	if reusesMany <= reusesFew {
		t.Fatalf("the %d-call program raised no more requests than the %d-call one (%d vs %d): the workload does not measure anything",
			many, few, reusesMany, reusesFew)
	}
	if scansMany != scansFew {
		t.Fatalf("descriptor scans grew with native call count: %d scans over %d requests vs %d over %d (want equal: the derivation is per program)",
			scansMany, derivationsMany+reusesMany, scansFew, derivationsFew+reusesFew)
	}
	if derivationsMany != derivationsFew {
		t.Fatalf("%d-call program derived the plan %d times, %d-call program %d times: the derivation is per call, not per program",
			few, derivationsFew, many, derivationsMany)
	}
	// The instrument must actually have counted the program's namespace, or an
	// unchanged zero would pass the equality above.
	if scansFew == 0 {
		t.Fatal("descriptor scan instrument counted nothing")
	}
}
