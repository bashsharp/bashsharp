// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func TestBashPPUnicodeIdentifiersRuntime(t *testing.T) {
	const src = `type 数据 struct { 值 int }
func 计算(参数 int) {
 结果２ := 参数 + 1
 结果２ = 结果２ + 1
 对象 := 数据{值: 7}
 字段 := 对象.值
 println(结果２, 字段)
}
计算(4)
`
	var out strings.Builder
	r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
	bashPPRun(t, r, src)
	qt.Assert(t, qt.Equals(out.String(), "6 7\n"))
}

func TestBashPPUnicodeSetObjectRoundTripAndValidation(t *testing.T) {
	r, err := interp.New(interp.Lang(syntax.LangBashPP))
	qt.Assert(t, qt.IsNil(err))
	want := map[string]any{"值": 7}
	qt.Assert(t, qt.IsNil(r.SetObject("数据２", want)))
	got, ok := r.Object("数据２")
	qt.Assert(t, qt.IsTrue(ok))
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Object Unicode round trip = %#v, want %#v", got, want)
	}
	for _, invalid := range []string{"for", "２数据", string([]byte{0xff})} {
		if err := r.SetObject(invalid, want); err == nil {
			t.Errorf("SetObject(%q) unexpectedly succeeded", invalid)
		}
	}
}
