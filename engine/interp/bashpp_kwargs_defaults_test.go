// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

package interp_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"github.com/go-quicktest/qt"
)

func runBashSharpCall(t *testing.T, src string) (stdout, stderr string, err error) {
	t.Helper()
	f, parseErr := syntax.NewParser(syntax.Variant(syntax.LangBashPP)).Parse(strings.NewReader(src), "fixture.bpp")
	qt.Assert(t, qt.IsNil(parseErr))
	var out, errs strings.Builder
	r := bashPPRunner(t, &out, interp.StdIO(nil, &out, &errs), interp.Lang(syntax.LangBashPP))
	err = r.Run(context.Background(), f)
	return out.String(), errs.String(), err
}

func TestBashPPDeepReadonlyFixtures(t *testing.T) {
	t.Run("object construction", func(t *testing.T) {
		var output strings.Builder
		r := bashPPRunner(t, &output, interp.Lang(syntax.LangBashPP))
		bashPPRun(t, r, `type Metadata struct { Name string }
type Config struct { Meta Metadata; Ports []int; Labels map[string]string }
cfg := Config{Meta: Metadata{Name: "prod"}, Ports: []int{80, 443}, Labels: map[string]string{"tier": "edge"}}
readonly cfg
alias := cfg
`)
		value, ok := r.Object("cfg")
		alias, aliasOK := r.Object("alias")
		qt.Assert(t, qt.Equals(ok, true))
		qt.Assert(t, qt.Equals(aliasOK, true))
		qt.Assert(t, qt.DeepEquals(alias, value))
		qt.Assert(t, qt.Equals(output.String(), ""))
	})
	positives := []struct{ name, src, want string }{
		{"deep read", `type Metadata struct { Name string }
type Config struct { Meta Metadata; Ports []int; Labels map[string]string }
cfg := Config{
 Meta: Metadata{Name: "prod"},
 Ports: []int{80, 443},
 Labels: map[string]string{"tier": "edge"},
}
readonly cfg
alias := cfg
printf '%s:%d:%s\n' alias.Meta.Name alias.Ports[1] alias.Labels["tier"]
`, "prod:443:edge\n"},
		{"mutable imported pointer", `import "net/url"
endpoint, _ := url.Parse("https://example.test/original")
endpoint.Host = "changed.test"
host := endpoint.Host
printf '%s\n' "$host"
`, "changed.test\n"},
		{"mutable imported pointer alias", `import "net/url"
endpoint, _ := url.Parse("https://example.test/original")
alias := endpoint
alias.Host = "changed.test"
host := endpoint.Host
aliasHost := alias.Host
printf '%s:%s\n' "$host" "$aliasHost"
`, "changed.test:changed.test\n"},
	}
	for _, tc := range positives {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.src)
			if err != nil {
				t.Fatalf("run: %v; stdout=%q stderr=%q", err, out, stderr)
			}
			qt.Assert(t, qt.Equals(out, tc.want))
			qt.Assert(t, qt.Equals(stderr, ""))
		})
	}

	errors := []struct{ name, src, want string }{
		{"root", `cfg := map[string]int{"port": 80}
readonly cfg
cfg = map[string]int{"port": 443}
`, "BASHPP-EREADONLY-MUTATION: cannot assign to readonly value \"cfg\"\n"},
		{"map", `cfg := map[string]map[string]int{"nested": {"port": 80}}
readonly cfg
cfg["nested"]["port"] = 443
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"cfg\" through map path [\"nested\"][\"port\"]\n"},
		{"slice", `cfg := map[string][]int{"ports": {80, 443}}
readonly cfg
cfg["ports"][0] = 8080
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"cfg\" through slice path [\"ports\"][0]\n"},
		{"struct", `type Config struct { Name string }
cfg := Config{Name: "prod"}
readonly cfg
cfg.Name = "dev"
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"cfg\" through field .Name\n"},
		{"alias", `cfg := map[string][]int{"ports": {80, 443}}
alias := cfg
readonly cfg
alias["ports"][0] = 8080
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"cfg\" through alias \"alias\" and path [\"ports\"][0]\n"},
		{"subshell", `cfg := map[string][]int{"ports": {80, 443}}
readonly cfg
(
 cfg["ports"][0] = 8080
)
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"cfg\" through slice path [\"ports\"][0]\n"},
		{"imported alias created before readonly", `import "net/url"
endpoint, _ := url.Parse("https://example.test/original")
alias := endpoint
readonly endpoint
alias.Host = "changed.test"
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"endpoint\" through alias \"alias\" and path .Host\n"},
		{"imported", `import "net/url"
endpoint, _ := url.Parse("https://example.test/original")
readonly endpoint
endpoint.Host = "changed.test"
`, "BASHPP-EREADONLY-MUTATION: cannot mutate readonly value \"endpoint\" through field .Host\n"},
	}
	for _, tc := range errors {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.src)
			qt.Assert(t, qt.Equals(out, ""))
			qt.Assert(t, qt.Equals(stderr, tc.want))
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
		})
	}
}

func TestBashPPKwargsDefaultsFixtures(t *testing.T) {
	positives := []struct{ name, src, want string }{
		{"reordered", "func greet(name string, retries int) {\n printf '%s:%d\\n' name retries\n}\ngreet(retries: 3, name: \"Ada\")\n", "Ada:3\n"},
		{"positional then named", "func pair(left string, right string) {\n printf '%s/%s\\n' left right\n}\npair(\"ordinary\", right: \"named\")\n", "ordinary/named\n"},
		{"omitted default", "func greet(name string, retries int = 3) {\n printf '%s:%d\\n' name retries\n}\ngreet(\"Ada\")\n", "Ada:3\n"},
		{"explicit default override", "func greet(name string, retries int = 3) {\n printf '%s:%d\\n' name retries\n}\ngreet(\"Ada\", 5)\n", "Ada:5\n"},
		{"named default override", "func greet(name string, retries int = 3) {\n printf '%s:%d\\n' name retries\n}\ngreet(\"Ada\", retries: 7)\n", "Ada:7\n"},
	}
	for _, tc := range positives {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.src)
			qt.Assert(t, qt.Equals(out, tc.want))
			qt.Assert(t, qt.Equals(stderr, ""))
			qt.Assert(t, qt.IsNil(err))
		})
	}

	errors := []struct{ name, src, want string }{
		{"unknown", "func greet(name string) {}\ngreet(who: \"Ada\")\n", "BASHPP-EKWARG-UNKNOWN: greet has no parameter named \"who\"\n"},
		{"duplicate name", "func greet(name string) {}\ngreet(name: \"Ada\", name: \"Grace\")\n", "BASHPP-EKWARG-DUPLICATE: argument \"name\" is supplied more than once\n"},
		{"duplicate binding", "func greet(name string) {}\ngreet(\"Ada\", name: \"Grace\")\n", "BASHPP-EARG-DUPLICATE-BINDING: parameter \"name\" is supplied positionally and by name\n"},
		{"kwargs missing", "func greet(name string, retries int) {}\ngreet(name: \"Ada\")\n", "BASHPP-EARG-MISSING: greet requires argument \"retries\"\n"},
		{"defaults missing", "func greet(name string, retries int = 3) {}\ngreet()\n", "BASHPP-EARG-MISSING: greet requires argument \"name\"\n"},
		{"too many", "func greet(name string, retries int = 3) {}\ngreet(\"Ada\", 3, 4)\n", "BASHPP-EARG-COUNT: greet accepts at most 2 arguments; got 3\n"},
		{"nontrailing default", "func greet(retries int = 3, name string) {}\n", "BASHPP-EDEFAULT-ORDER: required parameter \"name\" follows a default parameter\n"},
	}
	for _, tc := range errors {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr, err := runBashSharpCall(t, tc.src)
			qt.Assert(t, qt.Equals(out, ""))
			qt.Assert(t, qt.Equals(stderr, tc.want))
			qt.Assert(t, qt.ErrorIs(err, interp.ExitStatus(2)))
		})
	}
}
