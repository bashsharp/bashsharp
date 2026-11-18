//go:build full

package lower_test

import "testing"

// Exact public agentic/actions.bpp, with empty positional arguments in both executions.
func TestPublicAgenticActions(t *testing.T) {
	source := `# Every presentation retains ordinary input/output conventions.
agentic func twice(n int) int {
    return $((n * 2))
}
type Value int
agentic func (v Value) Show() {
    echo "method:$v"
}
type Shower interface { Show() }
agentic function shell_action() {
    printf 'shell:%s\n' "$1"
}
func ordinary(n int) int {
    return $((n + 1))
}
callback := func() {
    agentic {
        shell_action closure
    }
}
agentic {
    n := twice(21)
    echo "typed:$n"
    action := twice
    m := action(4)
    echo "value:$m"
    var v Value = 7
    v.Show()
    method := v.Show
    method()
    var view Shower = v
    view.Show()
    shell_action "$1"
    callback()
    x := ordinary(8)
    echo "ordinary:$x"
    printf '%s\n' tool | /usr/bin/tr a-z A-Z
}
`
	execute(t, compile(t, source))
}
