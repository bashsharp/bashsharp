package interp

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
import (
	"fmt"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/constant"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"
)

// GoSourceTest identifies an original test declaration, not a source-file count.
type GoSourceTest struct {
	Name, Source string
	Line, Column uint
}

// Tests discovers every top-level Test function in the original typed AST.
// Registration does not execute any body and does not count dynamic subtests.
func (s *GoSourceTestingSession) Tests() ([]GoSourceTest, error) {
	if s == nil || s.closed || s.runner.goSourceTesting != s {
		return nil, fmt.Errorf("gosource: testing session is closed or reset")
	}
	var tests []GoSourceTest
	for _, stmt := range s.program.GoSourceAST().Stmts {
		decl, ok := stmt.Cmd.(*syntax.BashPPFuncDecl)
		if !ok || decl.Receiver != nil || decl.Name == nil {
			continue
		}
		name := decl.Name.Value
		if !strings.HasPrefix(name, "Test") {
			continue
		}
		tail := strings.TrimPrefix(name, "Test")
		if tail != "" {
			first, _ := utf8.DecodeRuneInString(tail)
			if unicode.IsLower(first) {
				continue
			}
		}
		if name == "TestMain" {
			return nil, fmt.Errorf("gosource: TestMain requires a separate testing.M driver")
		}
		if len(decl.TypeParams) != 0 {
			return nil, fmt.Errorf("gosource: test %s must not have type parameters", name)
		}
		params := bashppParams(decl.Params)
		if len(params) != 1 || len(decl.Results) != 0 || params[0].variadic {
			return nil, fmt.Errorf("gosource: test %s must have signature func(*testing.T)", name)
		}
		declared := params[0].declared
		alias, typ, ok := strings.Cut(strings.TrimPrefix(declared, "*"), ".")
		if !strings.HasPrefix(declared, "*") || !ok || typ != "T" || s.runner.bashPPImports[alias] != "testing" {
			return nil, fmt.Errorf("gosource: test %s must have signature func(*testing.T)", name)
		}
		source, _, ok := s.program.SourceAt(decl.Pos())
		if !ok {
			return nil, fmt.Errorf("gosource: test %s lacks original source identity", name)
		}
		tests = append(tests, GoSourceTest{Name: name, Source: source, Line: decl.Pos().Line(), Column: decl.Pos().Col()})
	}
	return tests, nil
}

// Reflection only adapts testing's concrete callback type; it never executes
// original test code. testing remains owned by the host harness, whose Run and
// Cleanup implementations decide the callback goroutine and cleanup order.
func (r *Runner) bashPPTestingCallback(handle *goSourceTestingHandle, method string, args []any) (bool, error) {
	native := reflect.ValueOf(handle.target).MethodByName(method)
	if !native.IsValid() {
		return false, fmt.Errorf("gosource: scheduler does not provide testing.%s", method)
	}
	if method == "Run" {
		if len(args) != 2 {
			return false, fmt.Errorf("gosource: testing.Run requires a name and callback")
		}
		name, ok := args[0].(string)
		if !ok {
			return false, fmt.Errorf("gosource: testing.Run name must be a string")
		}
		fn, ok := args[1].(*bashPPFunc)
		if !ok {
			return false, fmt.Errorf("gosource: testing.Run callback must be interpreted")
		}
		if native.Type().NumIn() != 2 || native.Type().In(1).Kind() != reflect.Func || native.Type().In(1).NumIn() != 1 || native.Type().In(1).NumOut() != 0 || native.Type().NumOut() != 1 || native.Type().Out(0).Kind() != reflect.Bool {
			return false, fmt.Errorf("gosource: invalid scheduler Run signature")
		}
		ctx := r.ectx
		callback := reflect.MakeFunc(native.Type().In(1), func(values []reflect.Value) []reflect.Value {
			target, ok := values[0].Interface().(GoSourceTestingT)
			if !ok {
				panic("gosource: scheduler supplied invalid test capability")
			}
			if err := handle.session.runFunction(ctx, name, fn, target, handle.capabilityName(), true, nil); err != nil {
				target.Errorf("interpreted callback: %v", err)
			}
			return nil
		})
		result := native.Call([]reflect.Value{reflect.ValueOf(name), callback})
		return result[0].Bool(), nil
	}
	if len(args) != 1 {
		return false, fmt.Errorf("gosource: testing.Cleanup requires a callback")
	}
	fn, ok := args[0].(*bashPPFunc)
	if !ok {
		return false, fmt.Errorf("gosource: testing.Cleanup callback must be interpreted")
	}
	if native.Type().NumIn() != 1 || native.Type().In(0).Kind() != reflect.Func || native.Type().In(0).NumIn() != 0 {
		return false, fmt.Errorf("gosource: invalid scheduler Cleanup signature")
	}
	// The root callback's signal scope ends before Cleanup. The package session
	// lifetime remains valid until the harness closes the session.
	ctx := handle.session.context
	callback := reflect.MakeFunc(native.Type().In(0), func(_ []reflect.Value) []reflect.Value {
		if err := handle.session.runFunction(ctx, "cleanup", fn, handle.target, handle.capabilityName(), true, handle); err != nil {
			handle.target.Errorf("interpreted cleanup: %v", err)
		}
		return nil
	})
	native.Call([]reflect.Value{callback})
	return false, nil
}

// Run's and Loop's Boolean results participate in ordinary interpreted
// expressions — `if !t.Run(...)` and `for b.Loop()`.
func (r *Runner) bashPPTestingScalar(expr syntax.BashPPExpr) (bashPPScalar, bool, error) {
	call, ok := expr.(*syntax.BashPPCall)
	if !ok || len(call.Fun) != 2 {
		return bashPPScalar{}, false, nil
	}
	method := call.Fun[1].Value
	if method != "Run" && method != "Loop" {
		return bashPPScalar{}, false, nil
	}
	handle, args, handled, err := r.bashPPTestingArguments(call)
	if err != nil || !handled {
		return bashPPScalar{}, handled, err
	}
	if method == "Loop" {
		result, err := r.bashPPTestingLoop(handle, args)
		return bashPPScalar{value: constant.MakeBool(result), typ: "bool", runtime: true}, true, err
	}
	result, err := r.bashPPTestingCallback(handle, "Run", args)
	return bashPPScalar{value: constant.MakeBool(result), typ: "bool", runtime: true}, true, err
}
