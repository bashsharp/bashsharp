// Copyright (c) 2017, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package front

import (
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"
)

func envLookup(m map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		v, ok := m[name]
		return v, ok
	}
}

func TestResolveBashPP_Precedence(t *testing.T) {
	tests := []struct {
		name       string
		sel        BashPPSelector
		wantEnable bool
		wantSource BashPPSource
	}{
		{
			name: "bash binary, no signal anywhere: off by binary default",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "bashy binary, no signal anywhere: on by binary default",
			sel: BashPPSelector{
				Binary: BashPPBinaryBashy,
			},
			wantEnable: true,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "bash binary, .bpp extension stays off without a selector",
			sel: BashPPSelector{
				Binary:   BashPPBinaryBash,
				Filename: "script.bpp",
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "bashy binary records the .bpp extension tier",
			sel: BashPPSelector{
				Binary:   BashPPBinaryBashy,
				Filename: "script.bpp",
			},
			wantEnable: true,
			wantSource: BashPPSourceExtension,
		},
		{
			name: "bash binary, plain .sh extension stays off",
			sel: BashPPSelector{
				Binary:   BashPPBinaryBash,
				Filename: "script.sh",
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "env BASHY_BASHPP=1 beats extension-less default",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBash,
				LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "1"}),
			},
			wantEnable: true,
			wantSource: BashPPSourceEnv,
		},
		{
			name: "env BASHY_BASHPP=0 beats .bpp extension",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBash,
				Filename:  "script.bpp",
				LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "0"}),
			},
			wantEnable: false,
			wantSource: BashPPSourceEnv,
		},
		{
			name: "env BASHY_BASHPP=0 beats bashy binary default",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBashy,
				LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "0"}),
			},
			wantEnable: false,
			wantSource: BashPPSourceEnv,
		},
		{
			name: "env garbage value has no opinion, falls through to bash default",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBash,
				Filename:  "script.bpp",
				LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "yes"}),
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "env unset has no opinion, falls through to binary default",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBashy,
				LookupEnv: envLookup(map[string]string{"UNRELATED": "1"}),
			},
			wantEnable: true,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "CLI --bashpp beats env and extension",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBash,
				Args:      []string{"bash", "--bashpp", "script.sh"},
				LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "0"}),
			},
			wantEnable: true,
			wantSource: BashPPSourceCLI,
		},
		{
			name: "CLI --bash++ is an exact alias of --bashpp",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "--bash++", "script.sh"},
			},
			wantEnable: true,
			wantSource: BashPPSourceCLI,
		},
		{
			name: "CLI --no-bashpp beats .bpp extension and bashy default",
			sel: BashPPSelector{
				Binary:   BashPPBinaryBashy,
				Args:     []string{"bashy", "--no-bashpp", "script.bpp"},
				Filename: "script.bpp",
			},
			wantEnable: false,
			wantSource: BashPPSourceCLI,
		},
		{
			name: "CLI last-flag-wins: --bashpp then --no-bashpp resolves off",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "--bashpp", "--no-bashpp", "script.sh"},
			},
			wantEnable: false,
			wantSource: BashPPSourceCLI,
		},
		{
			name: "CLI last-flag-wins: --no-bashpp then --bashpp resolves on",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "--no-bashpp", "--bashpp", "script.sh"},
			},
			wantEnable: true,
			wantSource: BashPPSourceCLI,
		},
		{
			name: "CLI scan stops at the script operand",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "script.sh", "--bashpp"},
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "CLI scan stops at -c, its operand is not scanned",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "-c", "--bashpp"},
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "CLI scan stops at --",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "--", "--bashpp"},
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "bashy under --posix suppresses the binary default",
			sel: BashPPSelector{
				Binary: BashPPBinaryBashy,
				Posix:  true,
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "bashy under --posix --bashpp suppresses the explicit selector",
			sel: BashPPSelector{
				Binary: BashPPBinaryBashy,
				Args:   []string{"bashy", "--posix", "--bashpp"},
				Posix:  true,
			},
			wantEnable: false,
			wantSource: BashPPSourceCLI,
		},
		{
			name: "bash under --posix with no signal stays off, no refusal",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Posix:  true,
			},
			wantEnable: false,
			wantSource: BashPPSourceBinaryDefault,
		},
		{
			name: "bash under --posix --no-bashpp explicit off, no refusal, even with .bpp",
			sel: BashPPSelector{
				Binary:   BashPPBinaryBash,
				Args:     []string{"bash", "--posix", "--no-bashpp"},
				Filename: "script.bpp",
				Posix:    true,
			},
			wantEnable: false,
			wantSource: BashPPSourceCLI,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBashPP(tc.sel)
			if err != nil {
				t.Fatalf("ResolveBashPP() unexpected error: %v", err)
			}
			if got.Enabled != tc.wantEnable {
				t.Errorf("Enabled = %v, want %v", got.Enabled, tc.wantEnable)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

func TestResolveBashPP_POSIXInertnessProfile(t *testing.T) {
	tests := []struct {
		name       string
		sel        BashPPSelector
		wantPosix  bool
		wantSource BashPPSource
	}{
		{
			name: "bash --posix --bashpp selects the inert profile",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "--posix", "--bashpp"},
				Posix:  true,
			},
			wantSource: BashPPSourceCLI,
		},
		{
			name: "bash --posix --bash++ selects the inert profile",
			sel: BashPPSelector{
				Binary: BashPPBinaryBash,
				Args:   []string{"bash", "--posix", "--bash++"},
				Posix:  true,
			},
			wantSource: BashPPSourceCLI,
		},
		{
			name: "bash --posix with BASHY_BASHPP=1 selects the inert profile",
			sel: BashPPSelector{
				Binary:    BashPPBinaryBash,
				LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "1"}),
				Posix:     true,
			},
			wantSource: BashPPSourceEnv,
		},
		{
			name: "bash --posix with only a .bpp filename remains POSIX",
			sel: BashPPSelector{
				Binary:   BashPPBinaryBash,
				Filename: "script.bpp",
				Posix:    true,
			},
			wantPosix:  true,
			wantSource: BashPPSourceBinaryDefault,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveBashPP(tc.sel)
			if err != nil {
				t.Fatalf("ResolveBashPP() unexpected error: %v", err)
			}
			if got.Enabled {
				t.Errorf("Enabled = true, want false for the bash POSIX inertness profile")
			}
			if got.Posix != tc.wantPosix {
				t.Errorf("Posix = %v, want %v", got.Posix, tc.wantPosix)
			}
			if got.Source != tc.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tc.wantSource)
			}
		})
	}
}

func TestBashPPSource_Explicit(t *testing.T) {
	tests := []struct {
		source BashPPSource
		want   bool
	}{
		{BashPPSourceCLI, true},
		{BashPPSourceEnv, true},
		{BashPPSourceExtension, false},
		{BashPPSourceBinaryDefault, false},
	}
	for _, tc := range tests {
		if got := tc.source.Explicit(); got != tc.want {
			t.Errorf("%q.Explicit() = %v, want %v", tc.source, got, tc.want)
		}
	}
}

func TestBashPPResolution_ParserOptions(t *testing.T) {
	for _, tc := range []struct {
		name       string
		resolution BashPPResolution
		base       syntax.LangVariant
		wantBashPP bool
	}{
		{"bashpp", BashPPResolution{Enabled: true}, syntax.LangBash, true},
		{"bashpp-posix", BashPPResolution{Enabled: true, Posix: true}, syntax.LangBash, false},
		{"bashpp-posix-base", BashPPResolution{Enabled: true}, syntax.LangPOSIX, false},
		{"bashpp-base-posix", BashPPResolution{Posix: true}, syntax.LangBashPP, false},
		{"bash", BashPPResolution{}, syntax.LangBash, false},
		{"bash-posix", BashPPResolution{Posix: true}, syntax.LangBash, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := syntax.NewParser(tc.resolution.ParserOptions(tc.base)...)
			if _, err := p.Parse(strings.NewReader("echo ${x@Q}"), ""); err != nil {
				t.Fatalf("Bash grammar was not retained: %v", err)
			}
			_, err := p.Parse(strings.NewReader("agentic func f(n int) int { return n }"), "")
			if got := err == nil; got != tc.wantBashPP {
				t.Fatalf("Bash++ grammar accepted=%v, want %v (error %v)", got, tc.wantBashPP, err)
			}
		})
	}

	// POSIX mode must remain observable when it suppresses Bash++ grammar.
	// In POSIX mode, single quotes in these double-quoted parameter
	// expansions are literal text rather than quote syntax.
	resolved := BashPPResolution{Enabled: true, Posix: true}
	file, err := syntax.NewParser(resolved.ParserOptions(syntax.LangBash)...).
		Parse(strings.NewReader(`echo "${a+'x'}"`), "")
	if err != nil {
		t.Fatal(err)
	}
	call := file.Stmts[0].Cmd.(*syntax.CallExpr)
	dq := call.Args[1].Parts[0].(*syntax.DblQuoted)
	pe := dq.Parts[0].(*syntax.ParamExp)
	if got := pe.Exp.Word.Lit(); got != "'x'" {
		t.Fatalf("POSIX isolation dropped parsing semantics: got %q, want %q", got, "'x'")
	}
}

func TestResolveBashPP_POSIXGrammarContract(t *testing.T) {
	selectors := []struct {
		name   string
		args   []string
		env    map[string]string
		file   string
		on     bool
		source BashPPSource
	}{
		{"canonical", []string{"--bashpp"}, nil, "", true, BashPPSourceCLI},
		{"alias", []string{"--bash++"}, nil, "", true, BashPPSourceCLI},
		{"posix-before-selector", []string{"--posix", "--bashpp"}, nil, "", true, BashPPSourceCLI},
		{"selector-before-posix", []string{"--bash++", "--posix"}, nil, "", true, BashPPSourceCLI},
		{"last-selector-off", []string{"--bash++", "--no-bashpp"}, nil, "", false, BashPPSourceCLI},
		{"last-selector-on", []string{"--no-bashpp", "--bash++"}, nil, "", true, BashPPSourceCLI},
		{"environment-on", nil, map[string]string{"BASHY_BASHPP": "1"}, "", true, BashPPSourceEnv},
		{"environment-off", nil, map[string]string{"BASHY_BASHPP": "0"}, "script.bpp", false, BashPPSourceEnv},
		{"cli-on-beats-env-off", []string{"--bashpp"}, map[string]string{"BASHY_BASHPP": "0"}, "", true, BashPPSourceCLI},
		{"cli-off-beats-env-on", []string{"--no-bashpp"}, map[string]string{"BASHY_BASHPP": "1"}, "", false, BashPPSourceCLI},
		{"binary-default", nil, nil, "", false, BashPPSourceBinaryDefault},
		{"inferred-extension", nil, nil, "script.bpp", false, BashPPSourceBinaryDefault},
	}
	for _, binary := range []BashPPBinary{BashPPBinaryBash, BashPPBinaryBashy} {
		for _, startupPosix := range []bool{false, true} {
			for _, tc := range selectors {
				profile := "classic"
				if startupPosix {
					profile = "posix"
				}
				t.Run(string(binary)+"/"+profile+"/"+tc.name, func(t *testing.T) {
					args := append([]string{string(binary)}, tc.args...)
					res, err := ResolveBashPP(BashPPSelector{
						Binary: binary, Args: args, LookupEnv: envLookup(tc.env), Filename: tc.file, Posix: startupPosix,
					})
					if err != nil {
						t.Fatal(err)
					}
					on, source := tc.on, tc.source
					if source == BashPPSourceBinaryDefault && binary == BashPPBinaryBashy {
						on = true
						if tc.file != "" {
							source = BashPPSourceExtension
						}
					}
					wantEnable := on && !startupPosix
					wantPosix := startupPosix && (binary == BashPPBinaryBashy || !on)
					if res.Enabled != wantEnable || res.Posix != wantPosix || res.Source != source {
						t.Fatalf("resolution=%+v, want Enabled=%v Posix=%v Source=%s", res, wantEnable, wantPosix, source)
					}
					if res.Source.Explicit() != source.Explicit() {
						t.Fatal("POSIX policy changed explicit-selector provenance")
					}
					if got := res.LangVariant() == syntax.LangBashPP; got != wantEnable {
						t.Fatalf("runtime Bash++=%v, want %v", got, wantEnable)
					}
					_, err = syntax.NewParser(res.ParserOptions(syntax.LangBash)...).
						Parse(strings.NewReader("agentic func f(n int) int { return n }"), "contract")
					if got := err == nil; got != wantEnable {
						t.Fatalf("parser Bash++=%v, want %v (error %v)", got, wantEnable, err)
					}
				})
			}
		}
	}
}

func TestCommandLineBashPPSkipsOptionValues(t *testing.T) {
	for _, args := range [][]string{
		{"bash", "--rcfile", "foo", "--bashpp"},
		{"bash", "--init-file", "foo", "--bashpp"},
		{"bash", "-o", "errexit", "--bashpp"},
		{"bash", "-O", "extglob", "--bashpp"},
		{"bash", "-o", "errexit", "--bashpp", "--no-bashpp"},
	} {
		enabled, seen := CommandLineBashPP(args)
		want := args[len(args)-1] != "--no-bashpp"
		if !seen || enabled != want {
			t.Errorf("CommandLineBashPP(%q) = %v, %v; want %v, true", args, enabled, seen, want)
		}
	}
}

// TestBashSharpSpellingsAndDeprecatedAliases pins Sprint 212 D3: the Bash#
// spellings are canonical and the Bash++-era ones still resolve identically
// but are reported as deprecated, so a front can warn once.
func TestBashSharpSpellingsAndDeprecatedAliases(t *testing.T) {
	cases := []struct {
		name       string
		sel        BashPPSelector
		enabled    bool
		source     BashPPSource
		deprecated string
	}{
		{"--bashsharp", BashPPSelector{Binary: BashPPBinaryBash, Args: []string{"bash", "--bashsharp", "x"}}, true, BashPPSourceCLI, ""},
		{"--no-bashsharp", BashPPSelector{Binary: BashPPBinaryBashy, Args: []string{"bashy", "--no-bashsharp", "x"}}, false, BashPPSourceCLI, ""},
		{"--bashpp alias", BashPPSelector{Binary: BashPPBinaryBash, Args: []string{"bash", "--bashpp", "x"}}, true, BashPPSourceCLI, "--bashpp"},
		{"--bash++ alias", BashPPSelector{Binary: BashPPBinaryBash, Args: []string{"bash", "--bash++", "x"}}, true, BashPPSourceCLI, "--bash++"},
		{"BASHY_BASHSHARP", BashPPSelector{Binary: BashPPBinaryBash, LookupEnv: envLookup(map[string]string{"BASHY_BASHSHARP": "1"})}, true, BashPPSourceEnv, ""},
		{"BASHY_BASHPP alias", BashPPSelector{Binary: BashPPBinaryBash, LookupEnv: envLookup(map[string]string{"BASHY_BASHPP": "1"})}, true, BashPPSourceEnv, "BASHY_BASHPP"},
		{"BASHY_BASHSHARP wins over the alias", BashPPSelector{Binary: BashPPBinaryBash, LookupEnv: envLookup(map[string]string{"BASHY_BASHSHARP": "0", "BASHY_BASHPP": "1"})}, false, BashPPSourceEnv, ""},
		{".bsh", BashPPSelector{Binary: BashPPBinaryBashy, Filename: "prog.bsh"}, true, BashPPSourceExtension, ""},
		{".bpp alias", BashPPSelector{Binary: BashPPBinaryBashy, Filename: "prog.bpp"}, true, BashPPSourceExtension, ".bpp"},
	}
	for _, tc := range cases {
		res, err := ResolveBashPP(tc.sel)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if res.Enabled != tc.enabled || res.Source != tc.source || res.Deprecated != tc.deprecated {
			t.Errorf("%s: got enabled=%v source=%s deprecated=%q, want %v %s %q", tc.name, res.Enabled, res.Source, res.Deprecated, tc.enabled, tc.source, tc.deprecated)
		}
		if (res.DeprecationNotice() != "") != (tc.deprecated != "") {
			t.Errorf("%s: notice %q for deprecated %q", tc.name, res.DeprecationNotice(), tc.deprecated)
		}
	}
	if got := (BashPPResolution{Deprecated: "--bashpp"}).DeprecationNotice(); got != "warning: --bashpp is deprecated; use --bashsharp (the alias is removed after one minor release)" {
		t.Errorf("notice = %q", got)
	}
}
