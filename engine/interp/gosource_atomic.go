// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp

// Sprint: #118; Story: #67; Story-ID: 83b5cdc6fca6
//
// The integer functions of `sync/atomic` are answered by the interpreter, not
// by the dependency. See gosource_atomic.md: their whole subject is a variable
// the interpreter owns, which the dependency process cannot address at all.

import (
	"fmt"
	"go/constant"
	"go/token"
	"strconv"
	"strings"
	"sync"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// goSourceAtomicMutex serialises every interpreted sync/atomic operation.
//
// It is one lock for the process rather than one per variable, which is
// stronger than Go promises and therefore never wrong: the operations it
// guards are a read and a write of interpreter storage, they never block, and
// the sequential order it imposes is a legal order for a program whose only
// synchronisation on these words is atomic.
var goSourceAtomicMutex sync.Mutex

// goSourceAtomicWidth is the operand width of each claimed function name,
// paired with whether the operand is unsigned. Naming the operations by their
// exact Go spellings — rather than deriving them from a prefix — is what keeps
// the sibling families the dependency still owns (Pointer, Value, the typed
// atomic.Int64 methods) from being claimed by accident.
var goSourceAtomicWidth = map[string]struct {
	bits     int
	unsigned bool
}{
	"Int32": {32, false}, "Int64": {64, false},
	"Uint32": {32, true}, "Uint64": {64, true},
	"Uintptr": {strconv.IntSize, true},
}

// goSourceAtomicCall answers one `atomic.<Op><Type>(&x, …)` call over
// interpreter-owned storage, reporting whether it claimed the call.
//
// It claims nothing it cannot answer exactly. An unrecognised name, a first
// argument that is not an addressable interpreter word, a wrong argument count
// — each falls through to the ordinary native path and keeps that path's own
// diagnostic.
func (r *Runner) goSourceAtomicCall(call *syntax.BashPPCall) ([]bashPPBridgeValue, bool, error) {
	name, ok := r.goSourceAtomicSelector(call)
	if !ok {
		return nil, false, nil
	}
	op, typeName, ok := goSourceAtomicOperation(name)
	if !ok {
		return nil, false, nil
	}
	width := goSourceAtomicWidth[typeName]
	if len(call.ArgExprs) != len(call.Args) || len(call.ArgExprs) != goSourceAtomicArity(op) || call.Ellipsis.IsValid() {
		return nil, false, nil
	}
	ptr, err := r.bashPPPointerExprValue(call.ArgExprs[0])
	if err != nil {
		return nil, false, nil
	}
	if ptr == nil {
		return nil, true, fmt.Errorf("BASHPP-ENIL-DEREF: dereference of nil pointer")
	}
	operands := make([]constant.Value, 0, 2)
	for _, expr := range call.ArgExprs[1:] {
		scalar, err := r.bashPPEvalScalarExpr(expr)
		if err != nil {
			return nil, true, err
		}
		operands = append(operands, scalar.value)
	}
	goSourceAtomicMutex.Lock()
	defer goSourceAtomicMutex.Unlock()
	current, err := r.goSourceAtomicRead(ptr, width.bits, width.unsigned)
	if err != nil {
		return nil, true, err
	}
	result, stored, err := goSourceAtomicApply(op, current, operands, width.bits, width.unsigned)
	if err != nil {
		return nil, true, err
	}
	if stored != nil {
		if err := r.bashPPWriteBridgePointer(ptr, goSourceAtomicWord(stored, typeName, width.unsigned)); err != nil {
			return nil, true, err
		}
	}
	switch {
	case op == "Store":
		return nil, true, nil
	case op == "CompareAndSwap":
		return []bashPPBridgeValue{{Kind: "bool", Type: "bool", Text: strconv.FormatBool(stored != nil)}}, true, nil
	}
	return []bashPPBridgeValue{goSourceAtomicWord(result, typeName, width.unsigned)}, true, nil
}

// goSourceAtomicSelector returns the function name of a call on the sync/atomic
// package, in either spelling the parser produces.
func (r *Runner) goSourceAtomicSelector(call *syntax.BashPPCall) (string, bool) {
	if !r.bashPPGoSource || call == nil {
		return "", false
	}
	alias, name := "", ""
	if selector, ok := call.CalleeExpr.(*syntax.BashPPSelectorExpr); ok {
		id, ok := selector.X.(*syntax.BashPPIdent)
		if !ok {
			return "", false
		}
		alias, name = id.Name.Value, selector.Sel.Value
	} else if len(call.Fun) == 2 {
		alias, name = call.Fun[0].Value, call.Fun[1].Value
	} else {
		return "", false
	}
	if r.bashPPImports[alias] != "sync/atomic" {
		return "", false
	}
	// A local binding of the alias shadows the import, exactly as in Go.
	if r.bashPPScope != nil && r.bashPPScope.lookup(alias) != nil {
		return "", false
	}
	return name, true
}

// goSourceAtomicOperation splits AddUint64 into "Add" and "Uint64".
func goSourceAtomicOperation(name string) (string, string, bool) {
	for _, op := range []string{"CompareAndSwap", "Add", "Load", "Store", "Swap", "And", "Or"} {
		suffix, ok := strings.CutPrefix(name, op)
		if !ok {
			continue
		}
		if _, known := goSourceAtomicWidth[suffix]; !known {
			return "", "", false
		}
		return op, suffix, true
	}
	return "", "", false
}

func goSourceAtomicArity(op string) int {
	switch op {
	case "Load":
		return 1
	case "CompareAndSwap":
		return 3
	}
	return 2
}

// goSourceAtomicApply computes the operation's result and the word to store,
// which is nil when the operation stores nothing — a Load, or a failed
// CompareAndSwap.
func goSourceAtomicApply(op string, current constant.Value, operands []constant.Value, bits int, unsigned bool) (constant.Value, constant.Value, error) {
	switch op {
	case "Load":
		return current, nil, nil
	case "Store":
		return current, goSourceAtomicTruncate(operands[0], bits, unsigned), nil
	case "Swap":
		return current, goSourceAtomicTruncate(operands[0], bits, unsigned), nil
	case "Add":
		next := goSourceAtomicTruncate(constant.BinaryOp(current, token.ADD, operands[0]), bits, unsigned)
		return next, next, nil
	case "And":
		next := goSourceAtomicTruncate(constant.BinaryOp(current, token.AND, operands[0]), bits, unsigned)
		return current, next, nil
	case "Or":
		next := goSourceAtomicTruncate(constant.BinaryOp(current, token.OR, operands[0]), bits, unsigned)
		return current, next, nil
	case "CompareAndSwap":
		old := goSourceAtomicTruncate(operands[0], bits, unsigned)
		if constant.Compare(current, token.EQL, old) {
			return current, goSourceAtomicTruncate(operands[1], bits, unsigned), nil
		}
		return current, nil, nil
	}
	return nil, nil, fmt.Errorf("gosource: unsupported atomic operation %s", op)
}

// goSourceAtomicRead reads the addressed word, already reduced to the operand
// width so that a value written by other means still enters the operation as
// the machine word Go would have seen.
func (r *Runner) goSourceAtomicRead(ptr *bashPPPointer, bits int, unsigned bool) (constant.Value, error) {
	value, _, _, err := ptr.read()
	if err != nil {
		return nil, err
	}
	number := constant.MakeFromLiteral(fmt.Sprint(value), token.INT, 0)
	if number.Kind() != constant.Int {
		return nil, fmt.Errorf("gosource: atomic operand is not an integer word")
	}
	return goSourceAtomicTruncate(number, bits, unsigned), nil
}

// goSourceAtomicTruncate reduces a value to its operand width, which is what
// gives Go's wraparound: an Add past the maximum comes back around rather than
// growing an unbounded interpreter integer.
func goSourceAtomicTruncate(value constant.Value, bits int, unsigned bool) constant.Value {
	if unsigned {
		return constant.MakeUint64(goSourceAtomicUint(value, bits))
	}
	word := goSourceAtomicUint(value, bits)
	if bits < 64 && word&(1<<(bits-1)) != 0 {
		word |= ^uint64(0) << bits
	}
	return constant.MakeInt64(int64(word))
}

func goSourceAtomicUint(value constant.Value, bits int) uint64 {
	var word uint64
	if n, ok := constant.Uint64Val(value); ok {
		word = n
	} else if n, ok := constant.Int64Val(value); ok {
		word = uint64(n)
	}
	if bits < 64 {
		word &= 1<<bits - 1
	}
	return word
}

func goSourceAtomicWord(value constant.Value, typeName string, unsigned bool) bashPPBridgeValue {
	word := bashPPBridgeValue{Kind: "int", Type: strings.ToLower(typeName)}
	if unsigned {
		word.Kind = "uint"
	}
	word.Text = value.ExactString()
	return word
}
