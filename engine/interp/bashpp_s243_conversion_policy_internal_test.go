//go:build full

package interp

import (
	"context"
	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"go/constant"
	"math"
	"strings"
	"testing"
)

func TestS243ConversionPolicyIsRunnerScoped(t *testing.T) {
	value := bashPPScalar{value: constant.MakeFloat64(math.Ldexp(1, 63)), typ: "float64", runtime: true}
	ordinary := &Runner{bashPPGoSource: true}
	qy := &Runner{bashPPGoSource: true, bashPPConvertHashQY: true}

	gotOrdinary, err := ordinary.bashPPConvertScalar("int64", value)
	if err != nil {
		t.Fatal(err)
	}
	gotQY, err := qy.bashPPConvertScalar("int64", value)
	if err != nil {
		t.Fatal(err)
	}
	if gotOrdinary.value.ExactString() != "-9223372036854775808" {
		t.Fatalf("ordinary conversion = %s", gotOrdinary.value.ExactString())
	}
	if gotQY.value.ExactString() != "9223372036854775807" {
		t.Fatalf("qy conversion = %s", gotQY.value.ExactString())
	}
	if ordinary.bashPPConvertHashQY {
		t.Fatal("qy policy leaked to ordinary runner")
	}
}

func TestS243ConversionPolicyFlagAuthentication(t *testing.T) {
	for _, test := range []struct {
		flags string
		want  bool
	}{
		{"", false},
		{"-p=2", false},
		{"-d=converthash=qy", false},
		{"-gcflags=-d=converthash=qy", true},
		{"'-gcflags=-d=converthash=qy'", true},
		{"-gcflags=example.com/unrelated=-d=converthash=qy", false},
		{"-gcflags=all=-d=converthash=qy", false},
		{"-gcflags=-d=converthash=qySuffix", false},
		{"-gcflags=-d=converthash=xx", false},
		{"-gcflags=-d=converthash=qy -gcflags=-d=converthash=xx", false},
		{"-gcflags=-d=converthash=xx -gcflags=-d=converthash=qy", true},
		{"-gcflags=-d=converthash=qy -gcflags=example.com/unrelated=-d=converthash=xx", false},
		{"'-gcflags=-d=converthash=qy", false},
		{"-gcflags=-d=converthash=qy -gcflags=all=-d=converthash=xx", false},
		{"-gcflags=all=-d=converthash=qy -gcflags=-d=converthash=xx", false},
	} {
		if got := bashPPGoFlagsConvertHashQY(test.flags); got != test.want {
			t.Errorf("%q: got %v, want %v", test.flags, got, test.want)
		}
	}
}

func TestS243ConversionPolicyRejectsLinkedPackageScope(t *testing.T) {
	r, err := New(Lang(syntax.LangBashPP), Env(expand.ListEnviron("GOFLAGS=-gcflags=-d=converthash=qy")))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), &syntax.File{GoSource: true, Sources: []syntax.SourceFile{{PackagePath: "example.com/dep"}}})
	if err == nil || !strings.Contains(err.Error(), "unscoped conversion policy for linked packages") {
		t.Fatalf("got %v", err)
	}
	if r.bashPPConvertHashQY {
		t.Fatal("policy leaked after rejected file")
	}
}
