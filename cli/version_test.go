package cli

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/qiangli/bash/expand"
	"github.com/qiangli/bash/interp"
	"github.com/qiangli/bash/syntax"
)

func TestStartupUnderscore(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name         string
		inherited    []string
		want         string
		wantExported bool
	}{
		{name: "synthesized", want: exe},
		{name: "inherited", inherited: []string{"_=parent"}, want: "parent", wantExported: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := withBashVersionVars(expand.ListEnviron(test.inherited...), test.inherited)
			got := env.Get("_")
			if got.String() != test.want || got.Exported != test.wantExported {
				t.Fatalf("_ = %#v; want value %q exported=%v", got, test.want, test.wantExported)
			}
		})
	}
}

func TestBashVersionLineAppendsBuildID(t *testing.T) {
	oldVersion, oldBuildID := bashVersion, buildID
	defer func() {
		bashVersion, buildID = oldVersion, oldBuildID
	}()

	bashVersion = "5.3.0(1)-bashy-dev"
	buildID = "6e1d934-dirty"

	got := bashVersionLine()
	want := "5.3.0(1)-bashy-dev (6e1d934-dirty)"
	if got != want {
		t.Fatalf("bashVersionLine() = %q, want %q", got, want)
	}
}

func TestBashVersionLineOmitsEmptyBuildID(t *testing.T) {
	oldVersion, oldBuildID := bashVersion, buildID
	defer func() {
		bashVersion, buildID = oldVersion, oldBuildID
	}()

	bashVersion = "5.3.0(1)-bashy"
	buildID = " "

	got := bashVersionLine()
	if got != bashVersion {
		t.Fatalf("bashVersionLine() = %q, want %q", got, bashVersion)
	}
}

func TestVersionBannerDefaultsToGNUCompatibleBash(t *testing.T) {
	oldVersion, oldBuildID := bashVersion, buildID
	oldProduct, oldCompatibility := VersionProduct, VersionCompatibility
	defer func() {
		bashVersion, buildID = oldVersion, oldBuildID
		VersionProduct, VersionCompatibility = oldProduct, oldCompatibility
	}()

	bashVersion = "5.3.0(1)-bashy-dev"
	buildID = "abc1234"
	VersionProduct = "GNU bash"
	VersionCompatibility = ""
	if got, want := versionBanner(), "GNU bash, version 5.3.0(1)-bashy-dev (abc1234)"; got != want {
		t.Fatalf("versionBanner() = %q, want %q", got, want)
	}

	VersionProduct = "bashy"
	VersionCompatibility = "GNU Bash 5.3 compatible"
	if got, want := versionBanner(), "bashy, GNU Bash 5.3 compatible, version 5.3.0(1)-bashy-dev (abc1234)"; got != want {
		t.Fatalf("bashy versionBanner() = %q, want %q", got, want)
	}
}

func TestBashVersionVarsStayOutOfRecipeEnvironment(t *testing.T) {
	tests := []struct {
		name         string
		inherited    []string
		wantExported bool
	}{
		{name: "synthesized shell variables"},
		{
			name:         "inherited export attributes",
			inherited:    []string{"BASH=parent", "BASH_VERSION=parent-version"},
			wantExported: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := withBashVersionVars(expand.ListEnviron(test.inherited...), test.inherited)
			var childBash, childVersion bool
			capture := func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
				return func(ctx context.Context, args []string) error {
					hcEnv := interp.HandlerCtx(ctx).Env
					bashVar := hcEnv.Get("BASH")
					versionVar := hcEnv.Get("BASH_VERSION")
					if !bashVar.IsSet() || bashVar.String() == "parent" {
						t.Fatalf("BASH shell variable = %#v; want bashy's startup value", bashVar)
					}
					if !versionVar.IsSet() || versionVar.String() == "parent-version" {
						t.Fatalf("BASH_VERSION shell variable = %#v; want bashy's startup value", versionVar)
					}
					hcEnv.Each(func(name string, vr expand.Variable) bool {
						if vr.Exported {
							childBash = childBash || name == "BASH"
							childVersion = childVersion || name == "BASH_VERSION"
						}
						return true
					})
					return nil
				}
			}
			runner, err := interp.New(interp.Env(env), interp.ExecHandlers(capture))
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser().Parse(strings.NewReader("recipe-probe"), "")
			if err != nil {
				t.Fatal(err)
			}
			if err := runner.Run(context.Background(), file); err != nil {
				t.Fatal(err)
			}
			if childBash != test.wantExported || childVersion != test.wantExported {
				t.Fatalf("recipe environment exports BASH=%v BASH_VERSION=%v; want both %v",
					childBash, childVersion, test.wantExported)
			}
		})
	}
}

// A release stamp carries dots ("-bashy-v1.0.0-rc.1-dev"); scripts and the
// Bash 5.3 history fixture take ${BASH_VERSION%.*} and expect "5.3", so the
// stamp must stay out of BASH_VERSION. --version still shows it.
func TestBashVersionVarOmitsReleaseStamp(t *testing.T) {
	oldVersion := bashVersion
	defer func() { bashVersion = oldVersion }()

	for _, stamp := range []string{"5.3.0(1)-bashy", "5.3.0(1)-bashy-dev", "5.3.0(1)-bashy-v1.0.0-rc.1-dev", "5.3.0(1)-bashy-v0.31.0"} {
		bashVersion = stamp
		_, got, _ := strings.Cut(bashVersionVars()[1], "=")
		if got != "5.3.0(1)-bashy" {
			t.Fatalf("stamp %q: BASH_VERSION = %q, want %q", stamp, got, "5.3.0(1)-bashy")
		}
		if trimmed := got[:strings.LastIndex(got, ".")]; trimmed != "5.3" {
			t.Fatalf("stamp %q: ${BASH_VERSION%%.*} = %q, want 5.3", stamp, trimmed)
		}
		if !strings.Contains(bashVersionLine(), stamp) {
			t.Fatalf("stamp %q missing from --version line %q", stamp, bashVersionLine())
		}
	}
}
