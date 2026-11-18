// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package typedjson

import (
	"bytes"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBashPPMethodRoundTrip(t *testing.T) {
	const src = "func (p *Count) M(v int) {\n return\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := Encode(&data, f); err != nil {
		t.Fatal(err)
	}
	n, err := Decode(&data)
	if err != nil {
		t.Fatal(err)
	}
	d := n.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl)
	if d.Receiver == nil || !d.Receiver.Pointer || d.Receiver.RecvType.Value != "Count" {
		t.Fatalf("receiver after JSON round trip = %#v", d.Receiver)
	}
}

func TestBashPPGenericMethodRoundTrip(t *testing.T) {
	const src = "func (p *Box[T]) M(v T) T {\n return v\n}\n"
	f, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "")
	if err != nil {
		t.Fatal(err)
	}
	var data bytes.Buffer
	if err := Encode(&data, f); err != nil {
		t.Fatal(err)
	}
	n, err := Decode(bytes.NewReader(data.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	d := n.(*syntax.File).Stmts[0].Cmd.(*syntax.BashPPFuncDecl)
	if d.Receiver == nil || len(d.Receiver.TypeParams) != 1 || d.Receiver.TypeParams[0].Value != "T" {
		t.Fatalf("generic receiver after JSON round trip = %#v", d.Receiver)
	}
}
