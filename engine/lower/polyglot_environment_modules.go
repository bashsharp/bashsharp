package lower

import (
	"fmt"
	"strings"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

// Preserve the validated source graph, including dependency order, in the
// generated runtime plan. Qualify its struct type with the emitter's alias.
func (e *emitter) nativeModuleUnitsLiteral(units []polyglot.NativeModuleUnit) string {
	if units == nil {
		return "nil"
	}
	var out strings.Builder
	fmt.Fprintf(&out, "[]%spolyglot.NativeModuleUnit{", e.prefix)
	for _, unit := range units {
		fmt.Fprintf(&out, "{Name:%q,Kind:%q,Source:%q,Requires:%#v},", unit.Name, unit.Kind, unit.Source, unit.Requires)
	}
	out.WriteString("}")
	return out.String()
}
