package interp

// Sprint: #118; Story: #64; Story-ID: 45be321bddfb
//
// Benchmark discovery and the b.Loop() scheduler capability. The benchmark
// runner stays entirely native: it owns the iteration count, the timer and the
// reported result. The interpreter only asks the real *testing.B whether to run
// another iteration and executes the original body when it says yes.

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// GoSourceTestingB is the scheduler capability passed to an interpreted
// benchmark. A real *testing.B implements it.
type GoSourceTestingB interface {
	GoSourceTestingT
	Loop() bool
}

// GoSourceBenchmark identifies an original Benchmark declaration.
type GoSourceBenchmark struct {
	Name, Source string
	Line, Column uint
}

// capabilityName reports the imported testing type this handle stands for,
// defaulting to T for handles created before benchmarks existed.
func (h *goSourceTestingHandle) capabilityName() string {
	if h == nil || h.capability == "" {
		return "T"
	}
	return h.capability
}

// Benchmarks discovers every top-level Benchmark function in the original
// typed AST. Registration does not execute any body.
func (s *GoSourceTestingSession) Benchmarks() ([]GoSourceBenchmark, error) {
	if s == nil || s.closed || s.runner.goSourceTesting != s {
		return nil, fmt.Errorf("gosource: testing session is closed or reset")
	}
	var benchmarks []GoSourceBenchmark
	for _, stmt := range s.program.GoSourceAST().Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok || decl.Receiver != nil || decl.Name == nil {
			continue
		}
		name := decl.Name.Value
		tail, ok := strings.CutPrefix(name, "Benchmark")
		if !ok {
			continue
		}
		if tail != "" {
			if first, _ := utf8.DecodeRuneInString(tail); unicode.IsLower(first) {
				continue
			}
		}
		if len(decl.TypeParams) != 0 {
			return nil, fmt.Errorf("gosource: benchmark %s must not have type parameters", name)
		}
		params := bashppParams(decl.Params)
		if len(params) != 1 || len(decl.Results) != 0 || params[0].variadic {
			return nil, fmt.Errorf("gosource: benchmark %s must have signature func(*testing.B)", name)
		}
		declared := params[0].declared
		alias, typ, cut := strings.Cut(strings.TrimPrefix(declared, "*"), ".")
		if !strings.HasPrefix(declared, "*") || !cut || typ != "B" || s.runner.bashPPImports[alias] != "testing" {
			return nil, fmt.Errorf("gosource: benchmark %s must have signature func(*testing.B)", name)
		}
		source, _, ok := s.program.SourceAt(decl.Pos())
		if !ok {
			return nil, fmt.Errorf("gosource: benchmark %s lacks original source identity", name)
		}
		benchmarks = append(benchmarks, GoSourceBenchmark{Name: name, Source: source, Line: decl.Pos().Line(), Column: decl.Pos().Col()})
	}
	return benchmarks, nil
}

// RunBenchmark invokes one original func(*testing.B) body in the caller's
// benchmark goroutine. Generated testing.InternalBenchmark callbacks can call
// it directly; no original benchmark body is compiled into their harness.
func (s *GoSourceTestingSession) RunBenchmark(ctx context.Context, name string, target GoSourceTestingB) error {
	if s == nil || s.closed || s.runner.goSourceTesting != s {
		return fmt.Errorf("gosource: testing session is closed or reset")
	}
	if target == nil {
		return fmt.Errorf("gosource: nil benchmark scheduler capability")
	}
	return s.runFunction(ctx, name, s.runner.bashPPFuncs[name], target, "B", false, nil)
}

// bashPPTestingLoop asks the real scheduler for the next iteration. The
// capability is adapted by reflection, exactly as Run and Cleanup are, so this
// runtime never imports testing and never owns the iteration count itself.
func (r *Runner) bashPPTestingLoop(handle *goSourceTestingHandle, args []any) (bool, error) {
	if len(args) != 0 {
		return false, fmt.Errorf("gosource: testing.B.Loop takes no arguments")
	}
	native := reflect.ValueOf(handle.target).MethodByName("Loop")
	if !native.IsValid() {
		return false, fmt.Errorf("gosource: scheduler does not provide testing.%s.Loop", handle.capabilityName())
	}
	if native.Type().NumIn() != 0 || native.Type().NumOut() != 1 || native.Type().Out(0).Kind() != reflect.Bool {
		return false, fmt.Errorf("gosource: invalid scheduler Loop signature")
	}
	return native.Call(nil)[0].Bool(), nil
}
