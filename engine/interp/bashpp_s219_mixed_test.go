package interp_test

import (
	"strings"
	"testing"

	"github.com/go-quicktest/qt"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS219MixedImportedScalarArgument(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"local string", `import "strings"; s := "abc"; u := strings.ToUpper(s); printf '%s' "$u"`, "ABC"},
		{"undefined stays rejected", `import "strings"; u := strings.ToUpper(missing)`, "missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.StringContains(out.String(), tc.want))
		})
	}
}

func TestS219MixedFunctionReturnBuiltinCall(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"len parameter", `func f(s string) int { return len(s) }; n := f("abc"); printf '%s' "$n"`, "3"},
		{"shadowed len", `func len(s string) int { return 9 }; func f(s string) int { return len(s) }; n := f("abc"); printf '%s' "$n"`, "9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.Equals(out.String(), tc.want))
		})
	}
}

func TestS219MixedImportedStructuredSelector(t *testing.T) {
	for _, tc := range []struct {
		name, src, want string
	}{
		{"url host", `import "net/url"; u, _ := url.Parse("https://example.test/p"); host := u.Host; printf '%s' "$host"`, "example.test"},
		{"unknown field", `import "net/url"; u, _ := url.Parse("https://example.test/p"); bad := u.Missing`, "Missing"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out strings.Builder
			r := bashPPRunner(t, &out, interp.Lang(syntax.LangBashPP))
			bashPPRun(t, r, tc.src)
			qt.Assert(t, qt.StringContains(out.String(), tc.want))
		})
	}
}
