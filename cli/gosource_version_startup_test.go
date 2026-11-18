package cli

import (
	"testing"

	"github.com/bashsharp/bashsharp/front"
)

func TestRunGoSourceVersionReachesLoader(t *testing.T) {
	path := writeGoFixture(t, "package p\nvar _ = 0b101\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Check: true, Files: []string{path}, GoVersion: "go1.12"})
	called := false
	withGoSourceHook(t, func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		called = true
		if opts.RunMain || opts.GoVersion != "go1.12" {
			t.Fatalf("wrong checker options: %+v", opts)
		}
		return &front.GoSourceProgram{Package: "p"}, nil
	})
	if err := runGoSourceInvocation(); err != nil || !called {
		t.Fatalf("load: called=%v err=%v", called, err)
	}
}

func TestRunGoSourceTestBuiltinsReachesLoader(t *testing.T) {
	path := writeGoFixture(t, "package p\nfunc f() { assert(true) }\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Check: true, Files: []string{path}, TestBuiltins: true})
	called := false
	withGoSourceHook(t, func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		called = true
		if opts.RunMain || !opts.TestBuiltins {
			t.Fatalf("wrong checker options: %+v", opts)
		}
		return &front.GoSourceProgram{Package: "p"}, nil
	})
	if err := runGoSourceInvocation(); err != nil || !called {
		t.Fatalf("load: called=%v err=%v", called, err)
	}
}

func TestRunGoSourceCheckEnvironmentReachesLoader(t *testing.T) {
	path := writeGoFixture(t, "package p\nfunc f() {\nL:\n\tfor {\n\t}\n}\n")
	withGoSourceSelection(t, front.GoSourceResolution{Enabled: true, Check: true, Files: []string{path}, CheckerBranchErrors: true, CheckAfterSyntaxErrors: true})
	called := false
	withGoSourceHook(t, func(_ []front.GoSourceFile, opts front.GoSourceOptions) (*front.GoSourceProgram, error) {
		called = true
		if opts.RunMain || !opts.CheckerBranchErrors || !opts.CheckAfterSyntaxErrors {
			t.Fatalf("wrong checker options: %+v", opts)
		}
		return &front.GoSourceProgram{Package: "p"}, nil
	})
	if err := runGoSourceInvocation(); err != nil || !called {
		t.Fatalf("load: called=%v err=%v", called, err)
	}
}
