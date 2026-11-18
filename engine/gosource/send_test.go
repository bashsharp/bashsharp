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

func TestSendKeepsBothPositionedOperands(t *testing.T) {
	source := `package main
func pick(c chan int)chan int{return c}
func value()int{return 20}
func main(){c:=make(chan int,1);pick(c)<-value()+1}`
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
	var send *syntax.BashPPSend
	syntax.Walk(result.File, func(n syntax.Node) bool {
		if n, ok := n.(*syntax.BashPPSend); ok {
			send = n
		}
		return true
	})
	if send == nil || send.ChanExpr == nil || send.ValueExpr == nil {
		t.Fatal("lost positioned channel or RHS AST")
	}
	if _, ok := send.ChanExpr.(*syntax.BashPPCall); !ok {
		t.Fatalf("channel %T", send.ChanExpr)
	}
	if _, ok := send.ValueExpr.(*syntax.BashPPBinaryExpr); !ok {
		t.Fatalf("RHS %T", send.ValueExpr)
	}
	if send.Pos() != send.ChanExpr.Pos() || send.End() != send.ValueExpr.End() || send.Pos().Line() != 4 {
		t.Fatal("original positions not authoritative")
	}
	// Corrupt only legacy compatibility words. No original bytes are changed;
	// traversal, printing and native lowering must use the positioned AST.
	send.Chan = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "STALE_CHANNEL"}}}
	send.Value = &syntax.Word{Parts: []syntax.WordPart{&syntax.Lit{Value: "STALE_VALUE"}}}
	syntax.Walk(send, func(n syntax.Node) bool {
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
