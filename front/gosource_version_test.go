package front

import (
	"reflect"
	"strings"
	"testing"
)

func TestGoSourceVersionSelection(t *testing.T) {
	for _, flags := range [][]string{{"--go-version=go1.12"}, {"--go-version", "go1.12"}} {
		args := append([]string{"bashy"}, flags...)
		args = append(args, "--source=go", "--bashpp", "--check", "original.go", "--go-version=argument")
		remaining, sel, err := StripGoSourceInvocationFlags(args)
		if err != nil || sel.GoVersion != "go1.12" || !sel.GoVersionSeen {
			t.Fatalf("selection: %+v %v", sel, err)
		}
		if !reflect.DeepEqual(remaining, []string{"bashy", "--bashpp", "original.go", "--go-version=argument"}) {
			t.Fatalf("operand/program argument boundary changed: %v", remaining)
		}
		res, err := ResolveGoSource(sel, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true})
		if err != nil || res.GoVersion != "go1.12" || !res.Check {
			t.Fatalf("resolution: %+v %v", res, err)
		}
	}
	for _, args := range [][]string{{"bashy", "--go-version"}, {"bashy", "--go-version="}} {
		if _, _, err := StripGoSourceInvocationFlags(args); err == nil {
			t.Fatal("missing version accepted")
		}
	}
	if _, err := ResolveGoSource(GoSourceSelection{GoVersion: "go1.12", GoVersionSeen: true}, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true}); err == nil || !strings.Contains(err.Error(), "requires --source=go") {
		t.Fatalf("language version leaked to shell source: %v", err)
	}
}

func TestGoSourceTestBuiltinsSelection(t *testing.T) {
	for _, flag := range []string{"--go-test-builtins", "--go-test-builtins=true"} {
		args := []string{"bashy", flag, "--source=go", "--bashpp", "--check", "original.go"}
		remaining, sel, err := StripGoSourceInvocationFlags(args)
		if err != nil || !sel.TestBuiltins || !sel.TestBuiltinsSeen {
			t.Fatalf("%s selection: %+v %v", flag, sel, err)
		}
		if !reflect.DeepEqual(remaining, []string{"bashy", "--bashpp", "original.go"}) {
			t.Fatalf("%s remaining args: %v", flag, remaining)
		}
		res, err := ResolveGoSource(sel, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true})
		if err != nil || !res.TestBuiltins || !res.Check {
			t.Fatalf("%s resolution: %+v %v", flag, res, err)
		}
	}

	if _, err := ResolveGoSource(GoSourceSelection{TestBuiltins: true, TestBuiltinsSeen: true}, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true}); err == nil || !strings.Contains(err.Error(), "requires --source=go") {
		t.Fatalf("test checker environment leaked to shell source: %v", err)
	}
}

// Sprint: #154; Story: S154.1; Story-ID: 29abb27c8659
func TestGoSourceCheckEnvironmentSelection(t *testing.T) {
	for _, tc := range []struct {
		flag string
		get  func(GoSourceSelection) (bool, bool)
		res  func(GoSourceResolution) bool
		sel  GoSourceSelection
	}{
		{"--go-checker-branch-errors", func(s GoSourceSelection) (bool, bool) { return s.CheckerBranchErrors, s.CheckerBranchErrorsSeen },
			func(r GoSourceResolution) bool { return r.CheckerBranchErrors }, GoSourceSelection{CheckerBranchErrors: true, CheckerBranchErrorsSeen: true}},
		{"--go-checker-branch-errors=true", func(s GoSourceSelection) (bool, bool) { return s.CheckerBranchErrors, s.CheckerBranchErrorsSeen },
			func(r GoSourceResolution) bool { return r.CheckerBranchErrors }, GoSourceSelection{CheckerBranchErrors: true, CheckerBranchErrorsSeen: true}},
		{"--go-check-after-syntax-errors", func(s GoSourceSelection) (bool, bool) { return s.CheckAfterSyntaxErrors, s.CheckAfterSyntaxErrorsSeen },
			func(r GoSourceResolution) bool { return r.CheckAfterSyntaxErrors }, GoSourceSelection{CheckAfterSyntaxErrors: true, CheckAfterSyntaxErrorsSeen: true}},
		{"--go-check-after-syntax-errors=true", func(s GoSourceSelection) (bool, bool) { return s.CheckAfterSyntaxErrors, s.CheckAfterSyntaxErrorsSeen },
			func(r GoSourceResolution) bool { return r.CheckAfterSyntaxErrors }, GoSourceSelection{CheckAfterSyntaxErrors: true, CheckAfterSyntaxErrorsSeen: true}},
	} {
		args := []string{"bashy", tc.flag, "--source=go", "--bashpp", "--check", "original.go"}
		remaining, sel, err := StripGoSourceInvocationFlags(args)
		if on, seen := tc.get(sel); err != nil || !on || !seen {
			t.Fatalf("%s selection: %+v %v", tc.flag, sel, err)
		}
		if !reflect.DeepEqual(remaining, []string{"bashy", "--bashpp", "original.go"}) {
			t.Fatalf("%s remaining args: %v", tc.flag, remaining)
		}
		res, err := ResolveGoSource(sel, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true})
		if err != nil || !tc.res(res) || !res.Check {
			t.Fatalf("%s resolution: %+v %v", tc.flag, res, err)
		}
		if _, err := ResolveGoSource(tc.sel, GoSourceContext{Binary: BashPPBinaryBashy, BashPP: true}); err == nil || !strings.Contains(err.Error(), "requires --source=go") {
			t.Fatalf("%s leaked to shell source: %v", tc.flag, err)
		}
	}
	if _, _, err := StripGoSourceInvocationFlags([]string{"bashy", "--go-check-after-syntax-errors=false", "--source=go", "--check", "x.go"}); err == nil || !strings.Contains(err.Error(), "expected true") {
		t.Fatalf("=false accepted: %v", err)
	}
}
