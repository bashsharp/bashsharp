package lower

import (
	"fmt"
	"strconv"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// checkedValueSite contains source spelling, never a runtime value projection.
func (e *emitter) checkedValueSite(node syntax.Node, name string) string {
	e.bridge = true
	line, column, offset := uint(0), uint(0), uint(0)
	if node != nil {
		pos := node.Pos()
		line, column, offset = pos.Line(), pos.Col(), pos.Offset()
	}
	return fmt.Sprintf("%srt.ValueSite{File:%s, Name:%s, Line:%d, Column:%d, Offset:%d}", e.prefix, strconv.Quote(e.options.Origin), strconv.Quote(name), line, column, offset)
}

// checkedDeref leaves evaluation in its enclosing expression, including a lazy
// boolean RHS. The pointer is passed once; the result remains addressable.
func (e *emitter) checkedDeref(node syntax.Node, pointer, name string) string {
	if e.goSource {
		// Go source spells its own dereference; the operand is already
		// the source's operand, parenthesised only where the source was.
		return "*" + pointer
	}
	site := e.checkedValueSite(node, name)
	return "(*" + e.prefix + "rt.MustValue(" + e.prefix + "rt.CheckedPointer(" + pointer + ", " + site + ")))"
}

// checkedAssertion erases only the static interface restriction on Go's own
// assertion. The compiler must classify an impossible assertion before calling
// this hook, including comma-ok assertions. Native dynamic type identity stays.
func (e *emitter) checkedAssertion(node *syntax.BashPPTypeAssertExpr, operand, sourceType string, impossible, commaOK bool) (string, error) {
	target, err := e.typeExpr(node.Assert)
	if err != nil {
		return "", err
	}
	site := e.checkedValueSite(node, sourceType)
	rt := e.prefix + "rt."
	metadata := fmt.Sprintf("%sAssertion{Source:%s, Target:%s, Impossible:%t, Site:%s}", rt, strconv.Quote(sourceType), strconv.Quote(target), impossible, site)
	helper, unwrap := "Assert", "MustValue"
	if commaOK {
		helper, unwrap = "AssertOK", "MustAssertOK"
	}
	return rt + unwrap + "(" + rt + helper + "[" + target + "](any(" + operand + "), " + metadata + "))", nil
}

// checkedIndexRead captures sequence then index once, in source order. Pass the
// address of an addressable array so index side effects precede its element read.
// Slice and string expressions are passed directly. For an assignment,
// the caller must capture its addressable container and use checkedIndexOffset.
// Pointer-to-array nil checking belongs at its implicit dereference seam.
func (e *emitter) checkedIndexRead(node syntax.Node, sequence, index, elementType, name string) string {
	site := e.checkedValueSite(node, name)
	rt := e.prefix + "rt."
	return "func() " + elementType + " { " + e.prefix + "sequence := " + sequence + "; " + e.prefix + "index := " + index + "; return " + e.prefix + "sequence[" + rt + "MustValue(" + rt + "CheckIndex(" + e.prefix + "index, len(" + e.prefix + "sequence), " + site + "))] }()"
}
func (e *emitter) checkedIndexOffset(node syntax.Node, index, length, name string) string {
	site := e.checkedValueSite(node, name)
	return e.prefix + "rt.MustValue(" + e.prefix + "rt.CheckIndex(" + index + ", " + length + ", " + site + "))"
}
func (e *emitter) checkedMakeSlice(node syntax.Node, elementType, length, capacity string) string {
	site := e.checkedValueSite(node, "make")
	if capacity == "" {
		return e.prefix + "rt.MustValue(" + e.prefix + "rt.MakeSliceLength[" + elementType + "](" + length + ", " + site + "))"
	}
	return e.prefix + "rt.MustValue(" + e.prefix + "rt.MakeSlice[" + elementType + "](" + length + ", " + capacity + ", " + site + "))"
}
func (e *emitter) checkedMakeMap(node syntax.Node, keyType, valueType, size string) string {
	site := e.checkedValueSite(node, "make")
	return e.prefix + "rt.MustValue(" + e.prefix + "rt.MakeMap[" + keyType + ", " + valueType + "](" + size + ", " + site + "))"
}
