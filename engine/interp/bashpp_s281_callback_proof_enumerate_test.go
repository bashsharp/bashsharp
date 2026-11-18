//go:build full

package interp

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// The callback proof reports one refusal: the first region it cannot certify.
// That hides the true remaining scope, because every fix only uncovers the next
// site. This measurement drives the same proof with the enumerate hook, which
// turns each refusal into a local "assume refused and keep walking", so every
// distinct site the production proof would eventually reach is recorded in one
// run. It asserts nothing about the verdict: it is a report.

// dependencyCallbackEnumerationSiteBound and the raised step budget below only
// exist so the walk can see past the first refusal; production keeps
// dependencyCallbackProofStepBudget.
const dependencyCallbackEnumerationSiteBound = 4000
const dependencyCallbackEnumerationStepBudget = 400000

var dependencyCallbackEnumerationNames = []*regexp.Regexp{
	regexp.MustCompile(`\b(argument|arg|local|field|parameter|variable|label|function|method|name|callee|receiver) [A-Za-z_][A-Za-z0-9_.]*`),
	regexp.MustCompile(`"[^"]*"`),
	regexp.MustCompile(`-?\d+`),
}

// dependencyCallbackEnumerationClass folds the identifiers and counters out of
// a refusal reason so that the same rule refusing at many sites groups as one
// class.
func dependencyCallbackEnumerationClass(reason string) string {
	class := reason
	class = dependencyCallbackEnumerationNames[0].ReplaceAllString(class, "$1 X")
	class = dependencyCallbackEnumerationNames[1].ReplaceAllString(class, `"X"`)
	class = dependencyCallbackEnumerationNames[2].ReplaceAllString(class, "N")
	return class
}

type dependencyCallbackEnumerationSite struct {
	class    string
	function string
	position string
	sample   string
	hits     int
	order    int
}

type dependencyCallbackEnumeration struct {
	sites map[string]*dependencyCallbackEnumerationSite
	total int
}

func (e *dependencyCallbackEnumeration) record(site dependencyCallbackRefusalSite) bool {
	e.total++
	class := dependencyCallbackEnumerationClass(site.reason)
	key := site.position + "\x00" + site.function + "\x00" + class
	entry, ok := e.sites[key]
	if !ok {
		if len(e.sites) >= dependencyCallbackEnumerationSiteBound {
			return true
		}
		entry = &dependencyCallbackEnumerationSite{class: class, function: site.function, position: site.position, sample: site.line, order: len(e.sites)}
		e.sites[key] = entry
	}
	entry.hits++
	// Report the refusal as locally assumed and let the walk continue into the
	// siblings of the region that refused.
	return true
}

func (e *dependencyCallbackEnumeration) report(title string, steps int, root string) string {
	sites := make([]*dependencyCallbackEnumerationSite, 0, len(e.sites))
	for _, site := range e.sites {
		sites = append(sites, site)
	}
	byClass := make(map[string][]*dependencyCallbackEnumerationSite)
	for _, site := range sites {
		byClass[site.class] = append(byClass[site.class], site)
	}
	classes := make([]string, 0, len(byClass))
	for class := range byClass {
		classes = append(classes, class)
	}
	sort.Slice(classes, func(i, j int) bool {
		if len(byClass[classes[i]]) != len(byClass[classes[j]]) {
			return len(byClass[classes[i]]) > len(byClass[classes[j]])
		}
		return classes[i] < classes[j]
	})
	var b strings.Builder
	fmt.Fprintf(&b, "=== %s: %d distinct sites, %d refusals reached, %d steps, %d classes\n", title, len(sites), e.total, steps, len(classes))
	for _, class := range classes {
		group := byClass[class]
		hits := 0
		for _, site := range group {
			hits += site.hits
		}
		fmt.Fprintf(&b, "  [%d sites, %d hits] %s\n", len(group), hits, class)
		sort.Slice(group, func(i, j int) bool { return group[i].order < group[j].order })
		for _, site := range group {
			position := site.position
			if root != "" {
				position = strings.TrimPrefix(position, root+string(filepath.Separator))
			}
			fmt.Fprintf(&b, "      %-52s %-28s hits=%d\n", position, site.function, site.hits)
		}
	}
	return b.String()
}

// dependencyCallbackEnumerationSelected is the file set the manager proof
// fixtures use; the empty list means every non-test .go file of the package,
// which is what the end-to-end gate proves over.
var dependencyCallbackEnumerationSelected = []string{"syntax.go", "parser.go", "scanner.go", "source.go", "branches.go", "tokens.go"}

func dependencyCallbackEnumerationFiles(t *testing.T, selected []string) (*token.FileSet, []*ast.File, string) {
	t.Helper()
	dir := filepath.Join(runtime.GOROOT(), "src", "cmd", "compile", "internal", "syntax")
	if _, err := os.Stat(dir); err != nil {
		t.Skipf("SDK sources unavailable: %v", err)
	}
	if len(selected) == 0 {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("read %s: %v", dir, err)
		}
		for _, entry := range entries {
			name := entry.Name()
			if strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
				selected = append(selected, name)
			}
		}
	}
	fset := token.NewFileSet()
	files := make([]*ast.File, 0, len(selected))
	for _, name := range selected {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		files = append(files, file)
	}
	return fset, files, dir
}

// Sprint: #281; Story: #810; Story-ID: 48c1146a3ab0
func TestS281CallbackProofRefusalEnumeration(t *testing.T) {
	for _, set := range []struct {
		name     string
		selected []string
	}{
		{name: "selected", selected: dependencyCallbackEnumerationSelected},
		{name: "package", selected: nil},
	} {
		t.Run(set.name, func(t *testing.T) {
			dependencyCallbackEnumerate(t, set.selected)
		})
	}
}

func dependencyCallbackEnumerate(t *testing.T, selected []string) {
	fset, files, dir := dependencyCallbackEnumerationFiles(t, selected)
	for _, test := range []struct {
		name string
		fn   string
		args []int
	}{
		{name: "Parse", fn: "Parse", args: []int{2}},
		{name: "ParseFile", fn: "ParseFile", args: []int{1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			enumeration := &dependencyCallbackEnumeration{sites: make(map[string]*dependencyCallbackEnumerationSite)}
			proof := newDependencyCallbackProof(files)
			proof.fset = fset
			proof.stepLimit = dependencyCallbackEnumerationStepBudget
			proof.enumerate = enumeration.record
			proof.prove(test.fn, test.args)
			report := enumeration.report("syntax."+test.fn, proof.steps, dir)
			t.Log("\n" + report)
			if out := os.Getenv("BASHPP_CALLBACK_PROOF_ENUM_DIR"); out != "" {
				path := filepath.Join(out, "enumerate-"+strings.ReplaceAll(t.Name(), "/", "-")+".txt")
				if err := os.WriteFile(path, []byte(report), 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
		})
	}
}
