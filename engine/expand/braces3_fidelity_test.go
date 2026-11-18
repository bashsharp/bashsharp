// Copyright (c) 2026, Daniel Martí <mvdan@mvdan.cc>
// See LICENSE for licensing information

package expand

import (
	"reflect"
	"testing"

	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestBraces3Fidelity(t *testing.T) {
	t.Parallel()

	t.Run("ParamElemWithSuffix", func(t *testing.T) {
		word := parseCallArg(t, "echo {$a,b}_{c,d}", 1)
		got, err := Fields(&Config{Env: ListEnviron("a=A")}, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"b_c", "b_d"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	t.Run("EmptyListElemsElideBareWords", func(t *testing.T) {
		word := parseCallArg(t, `printf '%s\n' {X,,Y,}`, 2)
		got, err := Fields(nil, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"X", "Y"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	// bash keeps the empty fields of `{X,,Y,}` only when a quoted
	// empty suffix makes each result a quoted null word.
	t.Run("QuotedEmptySuffixKeepsBareEmpties", func(t *testing.T) {
		word := parseCallArg(t, `printf '%s\n' {X,,Y,}''`, 2)
		got, err := Fields(nil, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"X", "", "Y", ""}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	// An all-empty brace list expands to nothing in bash: every
	// alternative is a null word and is elided, leaving no fields.
	t.Run("AllEmptyListExpandsToNothing", func(t *testing.T) {
		word := parseCallArg(t, `printf '%s\n' {,,}`, 2)
		got, err := Fields(nil, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string(nil)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	// An empty alternative followed by an unquoted empty parameter is
	// dropped (field splitting removes it), but a quoted empty
	// parameter survives as an empty field.
	t.Run("UnquotedEmptyParamSuffixElides", func(t *testing.T) {
		word := parseCallArg(t, `printf '%s\n' {a,}$undef`, 2)
		got, err := Fields(&Config{Env: ListEnviron("")}, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	t.Run("QuotedEmptyParamSuffixKeeps", func(t *testing.T) {
		word := parseCallArg(t, `printf '%s\n' {a,}"$undef"`, 2)
		got, err := Fields(&Config{Env: ListEnviron("")}, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"a", ""}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	t.Run("BraceResultLeadingTilde", func(t *testing.T) {
		word := parseCallArg(t, "echo {foo~,~}/bar", 1)
		got, err := Fields(&Config{Env: ListEnviron("HOME=/home/bob")}, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"foo~/bar", "/home/bob/bar"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	t.Run("PrefixTildeBraceResults", func(t *testing.T) {
		word := parseCallArg(t, "echo ~{/src,root}", 1)
		got, err := Fields(&Config{Env: ListEnviron("HOME=/home/bob", "HOME root=/root")}, word)
		if err != nil {
			t.Fatal(err)
		}
		want := []string{"/home/bob/src", "/root"}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("want %q, got %q", want, got)
		}
	})

	t.Run("BacktickCharRangeReportsBashSubstitutionError", func(t *testing.T) {
		word := &syntax.Word{Parts: []syntax.WordPart{lit("-{z..A}-")}}
		syntax.SplitBraces(word)
		var gotErr error
		for w, err := range BracesSeq(nil, word) {
			if err != nil {
				gotErr = err
				break
			}
			_ = w
		}
		want := "bad substitution: no closing \"`\" in `-"
		if gotErr == nil || gotErr.Error() != want {
			t.Fatalf("want error %q, got %v", want, gotErr)
		}
	})
}
