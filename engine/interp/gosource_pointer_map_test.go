//go:build full

package interp_test

// Sprint: #118; Story: #52; Story-ID: d564bada90bb

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

// This pinned concurrent example permits independent crawl interleavings and
// statistic order. Validate its closed graph and causal edges before comparing;
// line sorting alone would accept a wait before its corresponding crawl.
func TestGoSourceOriginalPointerDefinedMapThreeModes(t *testing.T) {
	source := forwardTypeOriginal(t, "webcrawler.go.txt",
		"490f194b0e0610dde7196a8cb2ebec643586a6f05f94a1aabe7a29fb526b8882")
	typedSendThreeModesNormalized(t, source, func(stream string) string {
		if err := validatePointerMapCrawler(stream); err != nil {
			t.Fatal(err)
		}
		return "validated pinned crawler graph"
	})
}

func pointerMapCrawlerGraph() ([]string, [][4]string) {
	root := "https://golang.org/"
	urls := []string{root, root + "pkg/", root + "pkg/fmt/", root + "pkg/os/", root + "cmd/"}
	bodies := []string{"The Go Programming Language", "Packages", "Package fmt", "Package os"}
	links := [][]int{{1, 4}, {0, 4, 2, 3}, {0, 1}, {0, 1}}
	var lines []string
	var edges [][4]string
	visits := []int{1, 0, 0, 0, 0}
	for i, targets := range links {
		lines = append(lines, fmt.Sprintf("Found: %s %q", urls[i], bodies[i]))
		for j, child := range targets {
			crawl := fmt.Sprintf("-> Crawling child %d/%d of %s : %s.", j, len(targets), urls[i], urls[child])
			wait := fmt.Sprintf("<- [%s] %d/%d Waiting for child %s.", urls[i], j, len(targets), urls[child])
			lines = append(lines, crawl, wait)
			edges = append(edges, [4]string{fmt.Sprintf("Found: %s %q", urls[i], bodies[i]), crawl, wait, "<- Done with " + urls[i]})
			visits[child]++
		}
		lines = append(lines, "<- Done with "+urls[i])
	}
	lines = append(lines, "<- Error on "+urls[4]+": not found: "+urls[4])
	for i, n := range visits {
		for j := 1; j < n; j++ {
			lines = append(lines, "<- Done with "+urls[i]+", already fetched.")
		}
	}
	lines = append(lines, "Fetching stats", "--------------")
	for _, url := range urls[:4] {
		lines = append(lines, url+" was fetched")
	}
	lines = append(lines, urls[4]+" failed: not found: "+urls[4])
	return lines, edges
}

// Mirrors the source-bound Tour webcrawler_crawl contract: exact line
// multiplicities, topology, bodies, found<crawl<wait<done, and final statistics.
// This helper is intentionally specific to the original hash above.
func validatePointerMapCrawler(stream string) error {
	expected, edges := pointerMapCrawlerGraph()
	if !strings.HasSuffix(stream, "\n") {
		return fmt.Errorf("crawler: missing final newline")
	}
	lines := strings.Split(strings.TrimSuffix(stream, "\n"), "\n")
	counts := map[string]int{}
	at := map[string]int{}
	for i, line := range lines {
		counts[line]++
		at[line] = i
	}
	for _, line := range expected {
		counts[line]--
	}
	for line, n := range counts {
		if n != 0 {
			return fmt.Errorf("crawler: multiplicity %d for %q", n, line)
		}
	}
	header := at["Fetching stats"]
	if at["--------------"] != header+1 {
		return fmt.Errorf("crawler: stats header")
	}
	for _, line := range lines[:header] {
		if strings.Contains(line, " was fetched") || strings.Contains(line, " failed: ") {
			return fmt.Errorf("crawler: early stats")
		}
	}
	for _, line := range lines[header+2:] {
		if !strings.Contains(line, " was fetched") && !strings.Contains(line, " failed: ") {
			return fmt.Errorf("crawler: late crawl")
		}
	}
	for _, edge := range edges {
		for i := 1; i < 4; i++ {
			if at[edge[i-1]] >= at[edge[i]] {
				return fmt.Errorf("crawler: causal order %q before %q", edge[i-1], edge[i])
			}
		}
	}
	return nil
}

func TestGoSourcePointerMapCrawlerRejectsInvalidTraces(t *testing.T) {
	valid, edges := pointerMapCrawlerGraph()
	check := func(lines []string) error { return validatePointerMapCrawler(strings.Join(lines, "\n") + "\n") }
	if err := check(valid); err != nil {
		t.Fatal(err)
	}
	// Each swap preserves every byte and multiplicity but violates a causal edge.
	for _, pair := range [][2]string{{edges[0][0], edges[0][1]}, {edges[0][1], edges[0][2]}, {edges[0][2], edges[0][3]}, {"Fetching stats", valid[0]}} {
		lines := slices.Clone(valid)
		a, b := slices.Index(lines, pair[0]), slices.Index(lines, pair[1])
		lines[a], lines[b] = lines[b], lines[a]
		if check(lines) == nil {
			t.Fatalf("accepted invalid swap %q", pair)
		}
	}
	for _, lines := range [][]string{valid[1:], append(slices.Clone(valid), valid[0]), append(slices.Clone(valid), "unknown")} {
		if check(lines) == nil {
			t.Fatal("accepted missing, duplicate, or unknown line")
		}
	}
	// The original map iteration may permute just the complete final stats block.
	lines := slices.Clone(valid)
	slices.Reverse(lines[len(lines)-5:])
	if err := check(lines); err != nil {
		t.Fatal(err)
	}
}

// Authored controls for a pointer to a defined map type. The crawler original
// only reads through `(*f)[url]`; these pin down the rest of the value, update,
// alias and nil-error semantics that the same evaluator paths now serve, each
// against real Go.
func TestGoSourcePointerDefinedMapThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		// The exact shape the crawler needs: comma-ok lookup through a
		// dereferenced pointer to a defined map with a pointer element type,
		// for both a present and a missing key.
		"comma_ok_through_deref": `package main
import "fmt"
type result struct{body string}
type fetcher map[string]*result
func (f *fetcher) get(k string)(string,bool){if r,ok:=(*f)[k];ok{return r.body,true};return "",false}
func main(){f:=&fetcher{"a":&result{"A"}};fmt.Println(f.get("a"));fmt.Println(f.get("z"))}`,

		// A one-value index through a pointer, and the zero value a missing
		// key yields for a pointer element type.
		"value_through_deref": `package main
import "fmt"
type table map[string]int
type boxes map[string]*int
func main(){t:=table{"a":1};p:=&t;fmt.Println((*p)["a"],(*p)["z"],len(*p))
n:=7;b:=boxes{"n":&n};q:=&b;fmt.Println(*(*q)["n"],(*q)["missing"]==nil)}`,

		// Writing an element through the pointer. Map elements are not
		// addressable in Go, so this is an element write into shared storage,
		// not a write through an address.
		"update_through_deref": `package main
import "fmt"
type table map[string]int
func bump(t *table,k string){(*t)[k]=(*t)[k]+1}
func main(){t:=table{"a":1};bump(&t,"a");bump(&t,"b");fmt.Println(t["a"],t["b"],len(t))}`,

		// A map is a reference, so the pointer, the variable it points at and
		// a second pointer all observe one another's writes. Deleting through
		// the pointer is visible the same way.
		"alias_shares_storage": `package main
import "fmt"
type table map[string]int
func main(){t:=table{"a":1};p:=&t;q:=&t
(*p)["b"]=2
t["c"]=3
fmt.Println(len(*q),(*q)["a"],(*q)["b"],(*q)["c"])
delete(*p,"a")
fmt.Println(len(t),t["a"])
var alias table=*p
alias["d"]=4
fmt.Println((*q)["d"],len(*q))}`,

		// A nil map reached through a non-nil pointer is readable: it has
		// length zero and yields the element zero value for every key, and a
		// comma-ok lookup reports absent rather than failing. Assigning into
		// one is the error case, pinned separately in
		// TestGoSourcePointerDefinedMapNilDiagnostics.
		"nil_map_reads_through_deref": `package main
import "fmt"
type table map[string]int
type boxes map[string]*int
func main(){var empty table
p:=&empty
v,ok:=(*p)["a"]
fmt.Println(empty==nil,*p==nil,len(*p),v,ok)
var none boxes
q:=&none
b,found:=(*q)["a"]
fmt.Println(len(*q),b==nil,found)
ready:=table{}
(*p)=ready
(*p)["a"]=1
fmt.Println(len(ready),ready["a"],empty["a"])}`,

		// The same dereference-then-index shape on a defined slice type, where
		// the element genuinely is addressable, so the write goes through an
		// address rather than into map storage.
		"nested_slice_lhs_once": `package main
import "fmt"
type grid [][]int
func next()int{fmt.Println("outer");return 0}
func key()int{fmt.Println("index");return 0}
func value()int{fmt.Println("rhs");return 9}
func main(){g:=grid{{1}};p:=&g;(*p)[next()][key()]=value();fmt.Println(g[0][0])}`,
		"nested_map_lhs_once": `package main
import "fmt"
type grid []map[int]int
func next()int{fmt.Println("outer");return 0}
func key()int{fmt.Println("index");return 0}
func value()int{fmt.Println("rhs");return 9}
func main(){g:=grid{map[int]int{0:1}};p:=&g;(*p)[next()][key()]=value();fmt.Println(g[0][0])}`,
		"nested_address_lhs_once": `package main
import "fmt"
type grid [][]int
func next()int{fmt.Println("outer");return 0}
func key()int{fmt.Println("index");return 0}
func main(){g:=grid{{1}};p:=&g;q:=&(*p)[next()][key()];*q=9;fmt.Println(g[0][0])}`,
		"defined_slice_through_deref": `package main
import "fmt"
type bag []int
func main(){b:=bag{1,2,3};p:=&b
(*p)[1]=9
fmt.Println((*p)[0],(*p)[1],len(*p),b[1])
fmt.Println((*p)[1:3])}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// These controls pin the faults for nil map writes and nil pointer access.
// Both are Go's run-time panics, reported as an unrecovered panic when
// nothing recovers them; the nil map write raises the runtime's plainError
// `assignment to entry in nil map` (Sprint 243).
func TestGoSourcePointerDefinedMapNilDiagnostics(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{
			name: "assign_through_pointer_to_nil_map",
			source: `package main
type table map[string]int
func main(){var empty table;p:=&empty;(*p)["a"]=1}`,
			want: "panic: assignment to entry in nil map",
		},
		{
			name: "read_through_nil_pointer",
			source: `package main
import "fmt"
type table map[string]int
func main(){var p *table;fmt.Println((*p)["a"])}`,
			want: "panic: runtime error: invalid memory address or nil pointer dereference",
		},
		{
			name: "assign_through_nil_pointer",
			source: `package main
type table map[string]int
func main(){var p *table;(*p)["a"]=1}`,
			want: "panic: runtime error: invalid memory address or nil pointer dereference",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			program, err := gosource.Parse(strings.NewReader(tc.source), tc.name+".go", gosource.Options{RunMain: true})
			if err != nil {
				t.Fatalf("gosource rejected the control: %v", err)
			}
			var out, errout bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			err = runner.Run(ctx, program.File)
			if err == nil {
				t.Fatalf("nil control unexpectedly succeeded: out=%q", out.String())
			}
			diagnostic := err.Error() + errout.String()
			if strings.Contains(diagnostic, "BASHPP-ENONADDRESSABLE") || strings.Contains(diagnostic, "BASHPP-ESELECTOR-EXPR") {
				t.Fatalf("dereference reported as an expression gap rather than the nil fault: %s", diagnostic)
			}
			if !strings.Contains(diagnostic, tc.want) {
				t.Fatalf("want %q, got %q", tc.want, diagnostic)
			}
		})
	}
}
