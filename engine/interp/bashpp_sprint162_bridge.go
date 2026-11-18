package interp

import (
	"strings"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

// bashPPSprint162CallbackType resolves the original instantiated type behind
// a materialised helper type. Generic local types are emitted under a stable
// generated name, while callback selectors intentionally retain the generic
// declaration name so the interpreter can find the original method body. The
// receiver still needs the instantiated spelling to rebuild its fields.
func (r *Runner) bashPPSprint162CallbackType(recv bashPPBridgeValue, base string) syntax.BashPPTypeExpr {
	name := strings.TrimPrefix(recv.Type, "main.")
	for _, local := range r.bashPPLocalTypeDescriptors() {
		if local.Name == name && local.Callback == base && local.WireType != "" {
			if typ := syntax.BashPPTypeExprFromText(local.WireType); typ != nil {
				return typ
			}
		}
	}
	return &syntax.BashPPNamedType{Name: &syntax.Lit{Value: base}}
}
