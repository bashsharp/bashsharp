package gosource_test

// Sprint: #118; Story: #51; Story-ID: 825f8083451e
import (
	"bytes"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"strings"
	"testing"
)

func TestReceiveKeepsPositionedOperand(t *testing.T) {
	source := `package main
func pick(c chan int)chan int{return c}
func main(){c:=make(chan int,1);c<-1;select{case value:=<-pick(c):_=value;default:}}`
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
	result.File = decoded.(*syntax.File)
	if !result.File.GoSource {
		t.Fatal("typed JSON lost GoSource mode")
	}
	var recv *syntax.BashPPReceive
	syntax.Walk(result.File, func(n syntax.Node) bool {
		if n, ok := n.(*syntax.BashPPReceive); ok {
			recv = n
		}
		return true
	})
	if recv == nil || recv.ChanExpr == nil {
		t.Fatal("lost positioned receive operand")
	}
	if _, ok := recv.ChanExpr.(*syntax.BashPPCall); !ok {
		t.Fatalf("channel %T", recv.ChanExpr)
	}
	if recv.End() != recv.ChanExpr.End() || recv.Pos().Line() != 3 {
		t.Fatal("original positions not authoritative")
	}
	recv.Chan = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "STALE_CHANNEL"}}}
	syntax.Walk(recv, func(n syntax.Node) bool {
		if lit, ok := n.(*syntax.Lit); ok && strings.HasPrefix(lit.Value, "STALE_") {
			t.Fatal("walk visited shadow legacy word")
		}
		return true
	})
	var printed bytes.Buffer
	if err := syntax.NewPrinter().Print(&printed, result.File); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(printed.String(), "STALE_") {
		t.Fatal("printer ignored typed operands")
	}
	lowered, err := lower.Compile(result.File, lower.Options{Origin: "unchanged.go"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(lowered.Source, []byte("STALE_")) {
		t.Fatal("lowerer ignored typed operands")
	}
}
