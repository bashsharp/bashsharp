package lower

import (
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/polyglot"
)

func TestForeignVariadicScalarParameterTypes(t *testing.T) {
	e := &emitter{prefix: "test_"}
	export := polyglot.Export{Name: "leading", Signature: polyglot.Signature{Params: []string{"float64", "string"}, Variadic: true}}
	// Variadic exports keep their prior ingress contract, including fixed
	// leading parameters. Widening those to any while skipping conversion
	// would silently deliver strings to a scalar parameter.
	for _, source := range []string{e.foreignWrapper("", "module", export, false), e.foreignErrWrapper(0, "module", "py", export, false)} {
		if !strings.Contains(source, "arg0 float64,arg1 ...string") || strings.Contains(source, "arg0 any") {
			t.Fatalf("variadic scalar contract widened: %s", source)
		}
	}
}
