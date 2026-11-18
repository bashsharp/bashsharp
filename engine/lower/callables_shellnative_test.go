//go:build full

package lower_test

import "testing"

func TestNativeShellCallablePanic(t *testing.T) {
	source := `sf() { echo insf; panic(viasf); echo unreachable; }
func f() {
 defer func() {
  v := recover()
  echo "got=$v"
 }()
 sf
 echo unreachable
}
f()
echo end
`
	executeBuild(t, compile(t, source), "-race")
}

func TestNativeShellDefinitionLifetime(t *testing.T) {
	for name, source := range map[string]string{
		"ordinary_clears_scope": `agentic func marked() { echo forbidden }
sf() { println("plain"); marked(); }
agentic { sf; }
`,
		"marked_invocation": `agentic function sf() { println("marked"); }
agentic { sf; }
`,
		"redefinition": `sf() { panic(stale); }
sf() { echo current; }
sf
`,
		"definition_order": `sf
sf() { panic(unreachable); }
echo after
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
