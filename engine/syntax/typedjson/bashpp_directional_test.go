package typedjson_test

import (
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/bashsharp/bashsharp/engine/syntax/typedjson"
	"reflect"
	"strings"
	"testing"
)

func TestBashPPDirectionalChannelRoundTrip(t *testing.T) {
	source := "func f(send chan<- []int, recv <-chan int) <-chan int {\n return recv\n}\n"
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(source), "direction.bpp")
	if err != nil {
		t.Fatal(err)
	}
	var encoded strings.Builder
	if err := typedjson.Encode(&encoded, file); err != nil {
		t.Fatal(err)
	}
	for _, fragment := range []string{`"Type":"BashPPChanType"`, `"Direction":"send"`, `"Direction":"recv"`, `"Element"`, `"Arrow"`} {
		if !strings.Contains(encoded.String(), fragment) {
			t.Fatalf("missing %s", fragment)
		}
	}
	decoded, err := typedjson.Decode(strings.NewReader(encoded.String()))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(file, decoded) {
		t.Fatal("positioned channel types changed")
	}
}
