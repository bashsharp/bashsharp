// Copyright (c) 2026, the sh authors.
// See LICENSE for licensing information

package interp_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/expand"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// TestLocalePatternMatching exercises character classification, bracket
// ranges, and POSIX classes in [[ ]], case, and ${var#pattern} under a
// single-byte non-C locale (de_DE.ISO-8859-1) alongside its UTF-8 and
// C/POSIX counterparts. These are pure Go string comparisons against
// hardcoded locale tables (see pattern.Locale and internal.SingleByteLocale)
// rather than calls into the host's C library, so unlike TestRunnerRunConfirm
// (which shells out to a real bash and would need the locale actually
// installed) none of these cases need a locale-availability skip.
func TestLocalePatternMatching(t *testing.T) {
	t.Parallel()

	// 0xC9/0xE9 are ISO-8859-1 for É/é; UTF-8 encodes the same letters as
	// two-byte sequences that remain valid UTF-8 on their own.
	const (
		latin1UpperEAcute = "\xc9"
		latin1LowerEAcute = "\xe9"
	)

	tests := []struct {
		name string
		env  string
		src  string
		want string
	}{
		{
			"iso88591 raw upper byte matches [[:upper:]]",
			"LC_ALL=de_DE.ISO-8859-1",
			`[[ "` + latin1UpperEAcute + `" == [[:upper:]] ]] && echo YES || echo NO`,
			"YES\n",
		},
		{
			"iso88591 raw lower byte matches [[:lower:]]",
			"LC_ALL=de_DE.ISO-8859-1",
			`[[ "` + latin1LowerEAcute + `" == [[:lower:]] ]] && echo YES || echo NO`,
			"YES\n",
		},
		{
			"iso88591 raw upper byte does not match [[:lower:]]",
			"LC_ALL=de_DE.ISO-8859-1",
			`[[ "` + latin1UpperEAcute + `" == [[:lower:]] ]] && echo YES || echo NO`,
			"NO\n",
		},
		{
			"C locale leaves the same raw byte unclassified",
			"LC_ALL=C",
			`[[ "` + latin1UpperEAcute + `" == [[:upper:]] ]] && echo YES || echo NO`,
			"NO\n",
		},
		{
			"unset locale leaves the same raw byte unclassified",
			"",
			`[[ "` + latin1UpperEAcute + `" == [[:upper:]] ]] && echo YES || echo NO`,
			"NO\n",
		},
		{
			"utf-8 locale classifies the real rune",
			"LC_ALL=de_DE.UTF-8",
			`[[ "É" == [[:upper:]] ]] && echo YES || echo NO`,
			"YES\n",
		},
		{
			"case bracket range under iso88591",
			"LC_ALL=de_DE.ISO-8859-1",
			`case b in [a-c]) echo IN;; *) echo OUT;; esac`,
			"IN\n",
		},
		{
			"case posix class under iso88591",
			"LC_ALL=de_DE.ISO-8859-1",
			`case B in [[:lower:]]) echo LOW;; *) echo NOTLOW;; esac`,
			"NOTLOW\n",
		},
		{
			"param removal strips locale-classified upper prefix (iso88591)",
			"LC_ALL=de_DE.ISO-8859-1",
			`x="` + latin1UpperEAcute + `test"; echo -n "${x#[[:upper:]]}"`,
			"test",
		},
		{
			"param removal strips locale-classified upper prefix (utf-8)",
			"LC_ALL=de_DE.UTF-8",
			`x="Étest"; echo -n "${x#[[:upper:]]}"`,
			"test",
		},
		{
			"param removal leaves prefix alone under C locale",
			"LC_ALL=C",
			`x="Étest"; echo -n "${x#[[:upper:]]}"`,
			"Étest",
		},
		{
			// A UTF-8 declared charmap (even a "C" base locale) must
			// not be treated as a single-byte locale: guards against
			// misclassifying "C.UTF-8" and similar as ISO-8859-1-like.
			"C.UTF-8 keeps byte-level matching for invalid UTF-8 patterns",
			"LC_ALL=C.UTF-8",
			`euro=$'\342\202\254'; b=$'\202'; case $euro in *$b*) echo bytematch ;; *) echo mbchar ;; esac`,
			"bytematch\n",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(tc.src), "")
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			var envOpt interp.RunnerOption
			if tc.env == "" {
				envOpt = interp.Env(expand.ListEnviron())
			} else {
				envOpt = interp.Env(expand.ListEnviron(tc.env))
			}
			r, err := interp.New(envOpt, interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			if err := r.Run(context.Background(), file); err != nil {
				t.Fatalf("run error: %v", err)
			}
			if got := out.String(); got != tc.want {
				t.Fatalf("%s | %s\nwant: %q\ngot:  %q", tc.env, tc.src, tc.want, got)
			}
		})
	}
}
