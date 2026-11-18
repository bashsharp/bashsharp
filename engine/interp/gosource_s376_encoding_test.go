package interp

import (
	"context"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"runtime"
	"testing"
)

// Exercise repeated collection snapshots and slice preparation without helper
// startup or wire I/O obscuring allocations.
func s376EncodingFixture() (*Runner, []any, syntax.BashPPTypeExpr) {
	scalar := &syntax.BashPPNamedType{Name: &syntax.Lit{Value: "int"}}
	record := &syntax.BashPPStructType{Fields: []*syntax.BashPPField{{Names: []*syntax.Lit{{Value: "X"}, {Value: "Y"}}, FieldTypeExpr: scalar}}}
	typ := &syntax.BashPPCollectionType{Kind: "slice", Element: record}
	values := make([]any, 32)
	for i := range values {
		values[i] = map[string]any{"X": "1", "Y": "2"}
	}
	return &Runner{bashPPGoSource: true}, values, typ
}

func BenchmarkS376BridgeEncoding(b *testing.B) {
	r, values, typ := s376EncodingFixture()
	req := bashPPEvalRequest{CallbackOwner: r, Imports: map[string]string{"reflect": "reflect"}}
	release := goSourceRaiseGCPacing()
	defer release()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		v, err := r.bashPPBridgeCollection(values, nil, typ)
		if err != nil {
			b.Fatal(err)
		}
		q := bashPPBridgeRequest{Op: "call", Selector: "reflect.DeepEqual", Args: []bashPPBridgeValue{v}}
		if err := prepareNativeSliceBuffers(context.Background(), req, &q); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	b.ReportMetric(float64(m.HeapAlloc), "live-B")
	runtime.KeepAlive(values)
}

func TestS376BridgeEncodingRefresh(t *testing.T) {
	r, values, typ := s376EncodingFixture()
	v, err := r.bashPPBridgeCollection(values, nil, typ)
	if err != nil {
		t.Fatal(err)
	}
	values[31].(map[string]any)["Y"] = "99"
	q := bashPPBridgeRequest{Op: "call", Selector: "reflect.DeepEqual", Args: []bashPPBridgeValue{v}}
	req := bashPPEvalRequest{CallbackOwner: r, Imports: map[string]string{"reflect": "reflect"}}
	if err := prepareNativeSliceBuffers(context.Background(), req, &q); err != nil {
		t.Fatal(err)
	}
	if got := q.Args[0].Elements[31].Fields["Y"].Text; got != "99" {
		t.Fatalf("stale encoding: %s", got)
	}
	if len(q.Args[0].Elements) != 32 {
		t.Fatal("lost elements")
	}
}
