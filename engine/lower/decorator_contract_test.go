//go:build full

package lower_test

import "testing"

func TestDecoratorSourceContract(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"site", `func show(c *Call) { site := c.Site; println(site); c.Next(); }
@show()
func target() { :; }
target()
`, "input.bpp:4\n"},
		{"panic_defer", `func catch(c *Call) {
    defer func() {
        r := recover()
        echo "caught:[$r]"
        c.Status = 1
    }()
    c.Next()
}
func cleanup() { echo "defer:body" }
@catch()
func panic_maker() {
    defer cleanup()
    panic("boom")
}
panic_maker()
echo "status:[$?]"
`, "defer:body\ncaught:[boom]\nstatus:[1]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, stderr := execute(t, compile(t, tc.source))
			if out != tc.want || stderr != "" {
				t.Fatalf("stdout=%q stderr=%q, want stdout=%q", out, stderr, tc.want)
			}
		})
	}
}
