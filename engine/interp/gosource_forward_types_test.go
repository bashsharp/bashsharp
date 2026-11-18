//go:build full

package interp_test

// Sprint: #118; Story: #53; Story-ID: 99bd1de0093b
import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// forwardTypeOriginal returns Tour bytes after checking them against the digest
// recorded here, so a test can never be made to pass by editing its input.
func forwardTypeOriginal(t *testing.T, name, digest string) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("testdata/gosource-type-registry", name))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(source)); got != digest {
		t.Fatalf("original bytes changed: %s", got)
	}
	return string(source)
}

// The Tour's crawler exercise declares `type fakeFetcher map[string]*fakeResult`
// above `type fakeResult struct{...}`. Package scope covers the whole file in
// Go, so this is ordinary source that the runtime rejected as
// `undefined type: fakeResult` while it registered declarations in the order it
// executed them.
func TestGoSourceOriginalForwardTypeThreeModes(t *testing.T) {
	source := forwardTypeOriginal(t, "exercise-web-crawler.go.txt",
		"bd6226dcae3b663a3aa1cbf502d840357ac0efb6de46e7a66612ca2834cf5e37")
	typedSendThreeModes(t, source)
}

// `methods/errors.go` gives MyError a time.Time field. That used to leave the
// local type without a codec at all; imported field types are now materialised,
// so the original runs, and it is compared against real Go rather than pinned to
// a diagnostic. Sprint: #118; Story: #54; Story-ID: c3a60493cde9
// The original prints `time.Now()`, so the timestamp is the one thing two runs
// cannot agree on. It is elided only after being checked as a real time in Go's
// own Time.String layout; the surrounding text — which is what the codec had to
// get right — is still compared byte for byte.
func TestGoSourceOriginalMethodsErrorsThreeModes(t *testing.T) {
	source := forwardTypeOriginal(t, "methods-errors.go.txt",
		"a0825fe6310af87e48e7f46b5290814df190d1e9533d0132fd663d6bb7f3236e")
	observedStart := time.Now()
	typedSendThreeModesNormalized(t, source, func(stream string) string {
		normalized, err := normalizeMethodsErrorTime(stream, observedStart, time.Now())
		if err != nil {
			t.Fatalf("MyError clock: %v", err)
		}
		return normalized
	})
}

// The custom-errors example returns an interpreter-owned *argError through the
// predeclared error interface, then asks errors.AsType to inspect it inside the
// dependency bridge. AsType is synchronous and mutates only its own generic
// destination; the original Error body remains interpreted.
func TestGoSourceOriginalCustomErrorsThreeModes(t *testing.T) {
	source := forwardTypeOriginal(t, "custom-errors.go.txt",
		"88886635c0867df55b7fcc2680e2c5d346799129b3ea258e0b717d9170a40c3f")
	typedSendThreeModes(t, source)
}

// The cluster's last pinned gap, `solutions/webcrawler.go`, is no longer a gap:
// `(*f)[url]` on a defined map type now evaluates, so it is compared against
// real Go in all three modes by TestGoSourceOriginalPointerDefinedMapThreeModes
// instead of having its diagnostic pinned here. That is a strictly stronger
// guard, so TestGoSourceOriginalForwardTypeRemainingGaps -- whose whole table
// it was -- is gone rather than left iterating nothing.
// Sprint: #118; Story: #52; Story-ID: d564bada90bb

// Forward and recursive references across every position a named type can hold
// in a declaration, each checked against real Go.
func TestGoSourceForwardTypeDeclarationsThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"map_element_forward":   `package main;import "fmt";type table map[string]*row;type row struct{n int};func main(){t:=table{"a":&row{1}};fmt.Println(t["a"].n)}`,
		"slice_element_forward": `package main;import "fmt";type bag []leaf;type leaf struct{n int};func main(){b:=bag{{1},{2}};fmt.Println(len(b),b[1].n)}`,
		"struct_field_forward":  `package main;import "fmt";type outer struct{in *inner};type inner struct{n int};func main(){o:=outer{&inner{3}};fmt.Println(o.in.n)}`,
		// The chan declaration is what must register; a named channel type has
		// its own unrelated runtime gaps, so nothing here constructs one.
		"chan_element_forward": `package main;import "fmt";type pipe chan *item;type item struct{n int};func main(){fmt.Println(item{4}.n)}`,
		"alias_forward":        `package main;import "fmt";type ref = *cell;type cell struct{n int};func main(){var r ref=&cell{5};fmt.Println(r.n)}`,
		// Neither declaration can be moved ahead of the other, so ordering
		// declarations by dependency could not have fixed this.
		"mutual_pointer_recursion": `package main;import "fmt";type a struct{b *b};type b struct{a *a};func main(){x:=&a{};y:=&b{a:x};x.b=y;fmt.Println(x.b.a==x)}`,
		"self_pointer_recursion":   `package main;import "fmt";type node struct{next *node;n int};func main(){n2:=&node{n:2};n1:=&node{next:n2,n:1};fmt.Println(n1.n,n1.next.n)}`,
		"generic_self_reference":   `package main;import "fmt";type List[T any] struct{next *List[T];val T};func main(){l:=&List[int]{val:1};l.next=&List[int]{val:2};fmt.Println(l.val,l.next.val)}`,
		"method_on_forward_type":   `package main;import "fmt";type holder map[string]*payload;func (h holder) get(k string)int{return h[k].n};type payload struct{n int};func main(){fmt.Println(holder{"k":&payload{6}}.get("k"))}`,
		"const_uses_forward_type":  `package main;import "fmt";const limit size=7;type size int;func main(){fmt.Println(limit)}`,
		// Package initialization still runs in dependency order; only the type
		// registry is populated ahead of the statements.
		"initializer_order": `package main;import "fmt";var second=first+1;var first=1;type later struct{n int};var third=&later{second};func main(){fmt.Println(first,second,third.n)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// A type declared inside a function body is not a package declaration, so
// pre-registration must not hoist it: the package `seen` prints before the
// declaration and the local `seen` after it, each under its own bridge
// identity (Sprint 243, bashpp_s243_scoped_local_types.go).
func TestGoSourceForwardTypeRegistryLocalTypeNotHoisted(t *testing.T) {
	differGoSource(t, "package main;import \"fmt\";type seen struct{n int};func main(){fmt.Println(seen{1});type seen struct{s string};fmt.Println(seen{\"x\"})}\n", nil, "")
}

// Pre-registration must widen nothing but package scope. Each of these is
// rejected for a reason that survives it. Rejection may come from the type
// checker or from the runtime; what matters is that neither name is quietly
// resolved to something the source did not mean.
func TestGoSourceForwardTypeRegistryRejects(t *testing.T) {
	for name, tc := range map[string]struct {
		source string
		want   []string
	}{
		// Infinite value layouts, now reachable because both names resolve.
		"mutual_value_recursion": {
			`package main;type a struct{b b};type b struct{a a};func main(){}`,
			[]string{"cyclic type declaration", "invalid recursive type"},
		},
		"self_value_recursion": {
			`package main;type loop struct{next loop};func main(){}`,
			[]string{"cyclic type declaration", "invalid recursive type"},
		},
		// A named constraint is still enforced; only `any` is free.
		"named_constraint_violated": {
			`package main;import "fmt";type num interface{~int};func sum[T num](xs []T)T{var t T;for _,x:=range xs{t+=x};return t};func main(){fmt.Println(sum([]string{"a"}))}`,
			[]string{"does not satisfy", "num"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			accept := func(diagnostic string) {
				t.Helper()
				for _, want := range tc.want {
					if strings.Contains(diagnostic, want) {
						return
					}
				}
				t.Fatalf("want one of %q, got %q", tc.want, diagnostic)
			}
			program, err := gosource.Parse(strings.NewReader(tc.source), name+".go", gosource.Options{RunMain: true})
			if err != nil {
				accept(err.Error())
				return
			}
			var out, errout bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			err = runner.Run(ctx, program.File)
			if err == nil {
				t.Fatalf("accepted: %q", out.String())
			}
			accept(err.Error() + errout.String())
		})
	}
}

// Registration is a gosource-only rule. Classic Bash++ has no package scope, so
// a name must still be declared before it is used.
func TestBashPPClassicTypesStaySequential(t *testing.T) {
	for name, tc := range map[string]struct{ source, want string }{
		"forward_reference_still_undefined": {"type Fetcher map[string]*Result\ntype Result struct { n int }\n", "undefined type: Result"},
		"undefined_name_still_undefined":    {"type Broken Missing\n", "undefined type: Missing"},
		"redeclaration_still_refused":       {"type Dup int\ntype Dup int\n", "type Dup redeclared in this session"},
	} {
		t.Run(name, func(t *testing.T) {
			var out strings.Builder
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.StdIO(nil, &out, &out))
			if err != nil {
				t.Fatal(err)
			}
			file, err := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(tc.source), "classic.bpp")
			if err != nil {
				t.Fatal(err)
			}
			_ = runner.Run(context.Background(), file)
			if !strings.Contains(out.String(), tc.want) {
				t.Fatalf("classic Bash++ did not report %q: %q", tc.want, out.String())
			}
		})
	}
}

var methodsErrorMonotonic = regexp.MustCompile(`^[+-][0-9]+\.[0-9]{9}$`)

func normalizeMethodsErrorTime(stream string, start, end time.Time) (string, error) {
	if !strings.HasPrefix(stream, "at ") {
		return "", fmt.Errorf("missing original prefix: %q", stream)
	}
	stamp, rest, ok := strings.Cut(strings.TrimPrefix(stream, "at "), ", ")
	if !ok {
		return "", fmt.Errorf("missing timestamp terminator")
	}
	wall, mono, hasMono := strings.Cut(stamp, " m=")
	if !hasMono || !methodsErrorMonotonic.MatchString(mono) {
		return "", fmt.Errorf("invalid or missing monotonic reading: %q", stamp)
	}
	elapsed, err := strconv.ParseFloat(mono, 64)
	if err != nil || elapsed < 0 || elapsed > end.Sub(start).Seconds()+1 {
		return "", fmt.Errorf("monotonic reading outside observed run: %q", mono)
	}
	when, err := time.Parse("2006-01-02 15:04:05.999999999 -0700 MST", wall)
	if err != nil || when.Before(start) || when.After(end) {
		return "", fmt.Errorf("wall time outside observation window: %q", wall)
	}
	return "at <time>, " + rest, nil
}
func TestGoSourceMethodsErrorClockValidation(t *testing.T) {
	start := time.Now().Add(-time.Second)
	end := time.Now()
	stamp := end.Add(-time.Millisecond).Format("2006-01-02 15:04:05.999999999 -0700 MST")
	for _, bad := range []string{"0001-01-01 00:00:00 +0000 UTC m=+0.001000000", stamp, stamp + " m=garbage", stamp + " m=+999999.000000000", stamp + " m=-0.100000000", stamp + " m=+0.1"} {
		if _, err := normalizeMethodsErrorTime("at "+bad+", exact remainder\n", start, end); err == nil {
			t.Fatalf("bad clock accepted: %q", bad)
		}
	}
	got, err := normalizeMethodsErrorTime("at "+stamp+" m=+0.001000000, exact remainder\n", start, end)
	if err != nil || got != "at <time>, exact remainder\n" {
		t.Fatalf("clock/remainder: %q %v", got, err)
	}
}
