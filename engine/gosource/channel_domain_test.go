package gosource

// Sprint: #281; Story: #811; Story-ID: aa5c046bb543

import (
	"go/token"
	"go/types"
	"slices"
	"testing"
)

func recursiveChannelTypeGraph() (types.Type, []string) {
	pkg := types.NewPackage("example.test/channelgraph", "channelgraph")
	payloadName := types.NewTypeName(token.NoPos, pkg, "Payload", nil)
	payload := types.NewNamed(payloadName, types.NewStruct(nil, nil), nil)

	receiveName := types.NewTypeName(token.NoPos, pkg, "Receive", nil)
	receive := types.NewNamed(receiveName, types.NewChan(types.RecvOnly, payload), nil)

	nodeName := types.NewTypeName(token.NoPos, pkg, "Node", nil)
	node := types.NewNamed(nodeName, nil, nil)
	interfaceOnly := types.NewChan(types.SendOnly, types.Typ[types.Bool])
	method := types.NewFunc(token.NoPos, pkg, "Forward", types.NewSignatureType(
		nil,
		nil,
		nil,
		types.NewTuple(types.NewVar(token.NoPos, pkg, "next", types.NewPointer(node))),
		types.NewTuple(types.NewVar(token.NoPos, pkg, "out", interfaceOnly)),
		false,
	))
	api := types.NewInterfaceType([]*types.Func{method}, nil).Complete()
	node.SetUnderlying(types.NewStruct([]*types.Var{
		types.NewField(token.NoPos, pkg, "Next", types.NewPointer(node), false),
		types.NewField(token.NoPos, pkg, "Inbox", receive, false),
		types.NewField(token.NoPos, pkg, "API", api, false),
		types.NewField(token.NoPos, pkg, "Index", types.NewMap(
			types.NewChan(types.SendOnly, types.Typ[types.Int]),
			types.NewSlice(types.NewArray(types.NewPointer(node), 2)),
		), false),
	}, nil))

	want := []string{
		channelTypeKey(types.NewChan(types.SendRecv, payload)),
		channelTypeKey(types.NewChan(types.SendRecv, types.Typ[types.Int])),
		channelTypeKey(types.NewChan(types.SendRecv, types.Typ[types.Bool])),
	}
	slices.Sort(want)
	return node, want
}

func TestChannelTypeKeysRecursiveSharedGraph(t *testing.T) {
	graph, want := recursiveChannelTypeGraph()
	got := channelTypeKeys(graph)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("channel type keys = %q, want %q", got, want)
	}

	// Directional and named channel views must stay in the same provenance
	// domain as the bidirectional underlying shape.
	namedReceive := graph.Underlying().(*types.Struct).Field(1).Type()
	payload := namedReceive.Underlying().(*types.Chan).Elem()
	wantKey := channelTypeKey(types.NewChan(types.SendRecv, payload))
	if got := channelTypeKey(namedReceive); got != wantKey {
		t.Fatalf("named receive key = %q, want %q", got, wantKey)
	}
	for _, direction := range []types.ChanDir{types.SendRecv, types.SendOnly, types.RecvOnly} {
		if got := channelTypeKey(types.NewChan(direction, payload)); got != wantKey {
			t.Fatalf("direction %v key = %q, want %q", direction, got, wantKey)
		}
	}
}

func TestChannelTypeKeyMemoCompletedTopLevelIdentity(t *testing.T) {
	graph, want := recursiveChannelTypeGraph()
	memo := channelTypeKeyMemo{}
	got := memo.keys(graph)
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("memoized channel type keys = %q, want %q", got, want)
	}
	if len(memo) != 1 {
		t.Fatalf("memo contains %d entries, want only the completed top-level graph", len(memo))
	}

	empty := types.NewStruct(nil, nil)
	if got := memo.keys(empty); len(got) != 0 {
		t.Fatalf("empty graph keys = %q, want none", got)
	}
	memo[empty] = nil
	if got := memo.keys(empty); got != nil {
		t.Fatalf("cached empty result was recomputed as %#v", got)
	}

	first := types.NewSlice(types.Typ[types.String])
	second := types.NewSlice(types.Typ[types.String])
	memo.keys(first)
	memo.keys(second)
	if _, ok := memo[first]; !ok {
		t.Fatal("first exact type identity was not cached")
	}
	if _, ok := memo[second]; !ok {
		t.Fatal("second exact type identity was not cached separately")
	}
}

var channelTypeKeysBenchmarkSink []string

func BenchmarkChannelTypeKeysRepeatedSharedGraph(b *testing.B) {
	graph, _ := recursiveChannelTypeGraph()
	b.Run("completed-top-level-memo", func(b *testing.B) {
		memo := channelTypeKeyMemo{}
		memo.keys(graph)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			channelTypeKeysBenchmarkSink = memo.keys(graph)
		}
	})
	b.Run("full-recursive-scan", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			channelTypeKeysBenchmarkSink = channelTypeKeys(graph)
		}
	})
}
