//go:build full

package lower_test

import "testing"

func TestDynamicShellRegions(t *testing.T) {
	for name, source := range map[string]string{
		"outside_shell": `agentic function marked() { echo body-must-not-run; }
marked
`,
		"ordinary_shell": `agentic function marked() { echo body-must-not-run; }
helper() { marked; }
agentic { helper; }
`,
		"eval_state": `agentic function marked() { echo "$1"; }
agentic {
 value=retained
 eval 'marked eval'
 marked restored-caller
}
printf 'value:%s\n' "$value"
`,
		"restoration": `agentic function marked() { echo body-must-not-run; }
helper() { agentic { return 0; } }
helper
marked
`,
		"exit": `false || exit 7
echo unreachable
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
