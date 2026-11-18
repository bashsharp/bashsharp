package gosource_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb
import (
	"bytes"
	"context"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"strings"
	"testing"
)

func TestMakeKeepsPositionedSizeOperands(t *testing.T) {
	source := `package main
func size()int{return 2}
func main(){_=make([]int,size(),size()+3)}`
	result, err := gosource.Parse(strings.NewReader(source), "unchanged.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var encoded bytes.Buffer
	if err := typedjson.Encode(&encoded, result.File); err != nil {
		t.Fatal(err)
	}
	decoded, err := typedjson.Decode(&encoded)
	if err != nil {
		t.Fatal(err)
	}
	file := decoded.(*syntax.File)
	var makeCall *syntax.BashPPCall
	syntax.Walk(file, func(n syntax.Node) bool {
		if c, ok := n.(*syntax.BashPPCall); ok && len(c.Fun) == 1 && c.Fun[0].Value == "make" {
			makeCall = c
		}
		return true
	})
	if makeCall == nil || makeCall.ArgType == nil || len(makeCall.ArgExprs) != 3 || makeCall.ArgExprs[0] != nil {
		t.Fatal("lost make type/size schema")
	}
	if _, ok := makeCall.ArgExprs[1].(*syntax.BashPPCall); !ok {
		t.Fatalf("size %T", makeCall.ArgExprs[1])
	}
	if _, ok := makeCall.ArgExprs[2].(*syntax.BashPPBinaryExpr); !ok {
		t.Fatalf("capacity %T", makeCall.ArgExprs[2])
	}
	if makeCall.ArgExprs[1].Pos().Line() != 3 {
		t.Fatal("lost original position")
	}
	for i := 1; i < len(makeCall.Args); i++ {
		makeCall.Args[i] = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "STALE_SIZE"}}}
	}
	var printed bytes.Buffer
	if err := syntax.NewPrinter().Print(&printed, file); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(printed.String(), "STALE_SIZE") {
		t.Fatal("printer used stale compatibility size")
	}
	lowered, err := lower.Compile(file, lower.Options{Origin: "unchanged.go"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(lowered.Source, []byte("STALE_SIZE")) {
		t.Fatal("lowering used stale compatibility size")
	}
}

// Sprint: #118; Story: #3; Story-ID: fa07603b71dc
// A named scalar type such as `type ServerState int` must keep its underlying
// scalar identity when used as a map key: the literal, indexed assignment and
// lookup all encode keys over that scalar, instead of rejecting the named type.
func TestNamedScalarMapKey(t *testing.T) {
	src := `package main
import "fmt"
type ServerState int
const (Idle ServerState = iota; Busy)
func main(){states:=map[ServerState]string{Idle:"idle",Busy:"busy"};states[Busy]="running";fmt.Println(states[Idle],states[Busy])}`
	p, err := gosource.Parse(strings.NewReader(src), "enum.go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Run(context.Background(), p.File); err != nil {
		t.Fatalf("run: %v stderr=%q", err, stderr.String())
	}
	if want := "idle running\n"; out.String() != want || stderr.Len() > 0 {
		t.Fatalf("out=%q stderr=%q want=%q", out.String(), stderr.String(), want)
	}
}
