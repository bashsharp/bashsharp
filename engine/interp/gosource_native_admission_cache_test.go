//go:build full

package interp

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS243NativeAdmissionCache(t *testing.T) {
	var r *Runner
	var checks []error
	var prior *bashPPNativeSession
	var diagnostics strings.Builder
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		diagnostics.Write(p)
		if !strings.Contains(string(p), "probe") {
			return len(p), nil
		}
		s := r.bashPPTools.bridge
		if s == prior {
			checks = append(checks, fmt.Errorf("Reset reused dependency session"))
		}
		prior = s
		iface := syntax.BashPPTypeExprFromText("interface{ RGBA()(uint32,uint32,uint32,uint32) }").(*syntax.BashPPInterfaceType)
		admit := func(name string, target *syntax.BashPPInterfaceType) error {
			cell := r.bashPPScope.lookup(name)
			if cell == nil {
				return fmt.Errorf("%s: no binding at the probe", name)
			}
			claimed, err := r.goSourceNativeImplements(cell, cell.declType, target)
			if !claimed {
				return fmt.Errorf("%s not claimed", name)
			}
			return err
		}
		requestCell := r.bashPPScope.lookup("request")
		if requestCell == nil {
			checks = append(checks, fmt.Errorf("request: no binding at the probe"))
			return len(p), nil
		}
		requestValue, err := r.bashPPBridgeCell(requestCell)
		if err != nil {
			checks = append(checks, err)
		} else {
			field, err := r.bashPPNativeAccess(r.ectx, "receiver_field", requestValue, "Body")
			if err != nil {
				checks = append(checks, err)
			} else {
				if field.NativeTypeID != 0 {
					checks = append(checks, fmt.Errorf("mutable interface field received concrete type token"))
				}
				if _, ok := s.nativeAdmissionKey(field, "io.Writer"); ok {
					checks = append(checks, fmt.Errorf("mutable interface payload became cacheable"))
				}
			}
		}
		before := s.next.Load()
		if err := admit("first", iface); err != nil {
			checks = append(checks, err)
		}
		after := s.next.Load()
		if after == before {
			checks = append(checks, fmt.Errorf("first admission lacked authenticated query"))
		}
		if err := admit("second", iface); err != nil {
			checks = append(checks, err)
		}
		if s.next.Load() != after {
			checks = append(checks, fmt.Errorf("same concrete type repeated native RPC"))
		}
		// Evict the original handles with transient authenticated responses.
		// A still-live value remains usable through a real type-check request.
		for i := 0; i < goSourceNativeHandleTypeCacheLimit; i++ {
			s.rememberNativeHandleType(bashPPBridgeValue{Kind: "handle", Session: s.id, Handle: uint64(1<<60) + uint64(i), NativeTypeID: 7})
		}
		beforeEvicted := s.next.Load()
		if err := admit("first", iface); err != nil {
			checks = append(checks, fmt.Errorf("evicted live handle: %w", err))
		}
		if s.next.Load() == beforeEvicted {
			checks = append(checks, fmt.Errorf("evicted handle skipped the live type check"))
		}
		// Re-reading a value remains a real operation: cached type admission never
		// substitutes the first color's payload for another color's channel values.
		wrong := syntax.BashPPTypeExprFromText("interface{ RGBA()(uint16,uint16,uint16,uint16) }").(*syntax.BashPPInterfaceType)
		if err := admit("second", wrong); err == nil {
			checks = append(checks, fmt.Errorf("wrong signature reused successful admission"))
		}
		missing := syntax.BashPPTypeExprFromText("interface{ Missing() }").(*syntax.BashPPInterfaceType)
		if err := admit("second", missing); err == nil {
			checks = append(checks, fmt.Errorf("missing method admitted"))
		}
		readIface := syntax.BashPPTypeExprFromText("interface{ Read([]byte)(int,error) }").(*syntax.BashPPInterfaceType)
		if err := admit("reader", readIface); err != nil {
			checks = append(checks, err)
		}
		if err := admit("readerValue", readIface); err == nil {
			checks = append(checks, fmt.Errorf("pointer method set reused for value"))
		}
		defined, ok := r.bashPPInterfaceType(&syntax.BashPPNamedType{Name: &syntax.Lit{Value: "Different"}})
		if !ok {
			checks = append(checks, fmt.Errorf("defined interface missing"))
		} else if err := admit("reader", defined); err == nil {
			checks = append(checks, fmt.Errorf("defined byte parameter collapsed to byte"))
		}
		req, err := r.bashPPEvalRequest()
		if err != nil {
			checks = append(checks, err)
		} else {
			changed := req
			changed.Imports = map[string]string{"different": "bytes"}
			if err := s.begin(context.Background(), changed); err == nil {
				checks = append(checks, fmt.Errorf("registry change bypassed"))
			}
		}
		return len(p), nil
	})
	var err error
	r, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	// Every binding the probe reads is used after it: a Go local stops being
	// reachable at its last use, and the interpreter drops it there.
	src := `package main
import ("image/color";"strings";"net/http";"os")
type Byte byte
type Different interface { Read([]Byte)(int,error) }
var _ Different
func main(){request:=&http.Request{Body:os.Stdin};first:=color.RGBA{1,2,3,255};second:=color.RGBA{7,8,9,255};reader:=strings.NewReader("abc");readerValue:=strings.Reader{};println("probe");r,_,_,_:=second.RGBA();if r!=1799{panic("cached payload")};_ =first;_=readerValue;_ =request;_ =reader}`
	parsed, err := gosource.Parse(strings.NewReader(src), filepath.Join(r.Dir, "cache.go"), gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if i > 0 {
			r.Reset()
		}
		if err := r.Run(context.Background(), parsed.File); err != nil {
			t.Fatalf("%v: %s", err, diagnostics.String())
		}
	}
	for _, err := range checks {
		t.Error(err)
	}
}

func TestS243NativeAdmissionIdentity(t *testing.T) {
	s := &bashPPNativeSession{id: "one"}
	s.rememberNativeHandleType(bashPPBridgeValue{Kind: "handle", Session: "one", Handle: 1, NativeTypeID: 11})
	s.rememberNativeHandleType(bashPPBridgeValue{Kind: "handle", Session: "one", Handle: 2, NativeTypeID: 12})
	v := bashPPBridgeValue{Kind: "handle", Session: "one", Handle: 1, NativeTypeID: 999, Type: "same printed type"}
	key, ok := s.nativeAdmissionKey(v, "interface{F()}")
	if !ok || key.source != 11 {
		t.Fatal("caller metadata established source identity")
	}
	s.rememberNativeAdmission(key)
	v.Handle = 2
	other, _ := s.nativeAdmissionKey(v, key.destination)
	if s.interfaceAdmissions[other] {
		t.Fatal("distinct concrete types shared an admission")
	}
	v.Handle = 3
	if _, ok := s.nativeAdmissionKey(v, key.destination); ok {
		t.Fatal("unknown handle accepted from claimed metadata")
	}
	v.Handle = 1
	v.Session = "other"
	if _, ok := s.nativeAdmissionKey(v, key.destination); ok {
		t.Fatal("cross-session handle accepted")
	}
	newer := &bashPPNativeSession{id: "two"}
	newer.rememberNativeHandleType(bashPPBridgeValue{Kind: "handle", Session: "two", Handle: 1, NativeTypeID: 11})
	if newer.interfaceAdmissions[key] {
		t.Fatal("type token reused cache across sessions")
	}
	// Only concrete handle responses carry worker tokens: interface payloads
	// with no token stay uncached even if their printed type matches a hit.
	s.rememberNativeHandleType(bashPPBridgeValue{Kind: "handle", Session: "one", Handle: 4, Type: "same printed type"})
	v.Session = "one"
	v.Handle = 4
	if _, ok := s.nativeAdmissionKey(v, key.destination); ok {
		t.Fatal("unidentified interface payload cached")
	}
}
