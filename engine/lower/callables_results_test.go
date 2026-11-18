package lower_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestPointerResultAcceptance(t *testing.T) {
	// Unchanged source and exact observation from
	// interp/bashpp_story204_residual_test.go:TestBashPPTupleAssignFunctionResultsPreserveMetadata.
	const source = `type Box struct { N int }
func rich() (*Box, *Box) {
 p := new(Box)
 q := new(Box)
 p.N = 5
 q.N = 6
 return p, q
}
func main() {
 var p *Box
 var q *Box
 p, q = rich()
 pv := *p
 qv := *q
 printf '%s:%s' pv.N qv.N
}
main()
`
	var out, diagnostic bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &diagnostic))
	if err != nil {
		t.Fatal(err)
	}
	if err := runner.Run(context.Background(), parse(t, source, "input.bpp")); err != nil || out.String() != "5:6" || diagnostic.Len() != 0 {
		t.Fatalf("source stdout=%q stderr=%q status=%v", out.String(), diagnostic.String(), err)
	}
	actual, stderr := execute(t, compile(t, source))
	if actual != "5:6" || stderr != "" {
		t.Fatalf("artifact stdout=%q stderr=%q", actual, stderr)
	}
}
