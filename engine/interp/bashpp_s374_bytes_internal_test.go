package interp

import (
	"os/exec"
	"runtime"
	"testing"
)

// TestS374ByteArrayBytesExtraction proves byte arrays cross the bridge in
// compact form: every storable byte spelling extracts to its raw image, and
// anything else falls back to the per-element path instead of corrupting.
func TestS374ByteArrayBytesExtraction(t *testing.T) {
	good, ok := bashPPByteArrayBytes([]any{"0", "1", 2, int64(3), uint8(4), uint64(255)})
	if !ok || string(good) != "\x00\x01\x02\x03\x04\xff" {
		t.Fatalf("extraction = %q,%v; want raw bytes,true", good, ok)
	}
	for name, value := range map[string][]any{
		"bool":      {"0", true},
		"nil":       {"0", nil},
		"float":     {"0", 1.5},
		"outrange":  {"0", 256},
		"negative":  {"0", -1},
		"unparse":   {"0", "zz"},
		"overbyte":  {"0", "256"},
		"emptytext": {"0", ""},
	} {
		if raw, ok := bashPPByteArrayBytes(value); ok {
			t.Fatalf("%s extracted to %q, want fallback", name, raw)
		}
	}
	if _, ok := bashPPByteArrayBytes(nil); !ok {
		t.Fatal("nil array does not extract")
	}
}

// TestS374MemStatsMemberScalar proves scalar field reads on a MemStats
// handle observe the host heap, while anything else falls through to the
// ordinary worker path.
func TestS374MemStatsMemberScalar(t *testing.T) {
	recv := bashPPBridgeValue{Kind: "handle", Type: "runtime.MemStats", Handle: 1}
	v, ok := bashPPHostMemStatsMember("s", recv, "Alloc")
	if !ok || v.Kind != "uint" {
		t.Fatalf("Alloc = %+v,%v; want uint,true", v, ok)
	}
	// A fixed snapshot proves the field conversion exactly. Comparing two
	// live heap reads would depend on allocation and GC between the reads.
	st := runtime.MemStats{Alloc: 123456789}
	if got, ok := bashPPMemStatsScalar(st, "Alloc"); !ok || got.Kind != "uint" || got.Text != "123456789" {
		t.Fatalf("fixed Alloc = %+v,%v", got, ok)
	}
	if v, ok := bashPPHostMemStatsMember("s", recv, "NumGC"); !ok || v.Kind != "uint" {
		t.Fatalf("NumGC = %+v,%v; want uint,true", v, ok)
	}
	for name, args := range map[string]struct {
		recv  bashPPBridgeValue
		field string
	}{
		"unknown field":    {recv, "NoSuchField"},
		"non-scalar field": {recv, "BySize"},
		"wrong kind":       {bashPPBridgeValue{Kind: "struct", Type: "runtime.MemStats"}, "Alloc"},
		"wrong type":       {bashPPBridgeValue{Kind: "handle", Type: "time.Time"}, "Alloc"},
		"foreign session":  {bashPPBridgeValue{Kind: "handle", Type: "runtime.MemStats", Session: "other"}, "Alloc"},
		"empty receiver":   {bashPPBridgeValue{}, "Alloc"},
	} {
		if _, ok := bashPPHostMemStatsMember("s", args.recv, args.field); ok {
			t.Fatalf("%s did not fall through", name)
		}
	}
}

// TestS374ByteTransportWorkerCompact proves the generated worker decodes a
// compact byte array, snapshots it without rendering, and reports only
// real changes back.
func TestS374ByteTransportWorkerCompact(t *testing.T) {
	binary := s374OwnershipWorker(t, `
func main() {
 bridgeAuth="0123456789abcdef0123456789abcdef"
 typ := resolveType("*[4]uint8")
 if typ == nil { panic("no *[4]uint8 type") }
 v, err := decode(value{Kind:"pointer", Type:"*[4]uint8", Origin:7, Session:bridgeAuth[:16], Elements:[]value{{Kind:"bytes", Type:"[4]uint8", Bytes:[]byte{1,2,3,4}}}}, typ)
 if err != nil { panic(err) }
 if v.Elem().Len() != 4 || v.Elem().Index(0).Uint() != 1 || v.Elem().Index(3).Uint() != 4 { panic("bad decode") }
 originalPointers.Lock(); stored := originalPointers.values[7]; sent := originalPointers.sent[7]; originalPointers.Unlock()
 if !stored.IsValid() { panic("no stored origin") }
 if sent != string([]byte{1,2,3,4}) { panic(fmt.Sprintf("bad snapshot %q", sent)) }
 raw, ok := pointeeBytes(stored)
 if !ok || string(raw) != string([]byte{1,2,3,4}) { panic("bad pointee bytes") }
 if update, changed := bytePointeeUpdate(7, stored, raw); changed { panic(fmt.Sprintf("unchanged reports change: %+v", update)) }
 stored.Elem().Index(0).SetUint(9)
 raw, ok = pointeeBytes(stored)
 if !ok { panic("bytes lost after mutation") }
 update, changed := bytePointeeUpdate(7, stored, raw)
 if !changed { panic("mutation reports no change") }
 if update.Kind != "pointer" || len(update.Elements) != 1 || string(update.Elements[0].Bytes) != string([]byte{9,2,3,4}) {
  panic(fmt.Sprintf("bad update %+v", update))
 }
 fmt.Println("compact ok")
}
`)
	if output, err := exec.Command(binary).CombinedOutput(); err != nil {
		t.Fatalf("worker: %v: %s", err, output)
	} else {
		t.Log(string(output))
	}
}
