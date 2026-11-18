//go:build full

package lower_test

import "testing"

func TestCompiledChannelCarriers(t *testing.T) {
	const source = `
func relay(ch) { ch <- function; }
func namedRelay(ch) { ch <- named; }
func defaultRelay(ch string = channel) { ch <- default; }
func main() {
 ch := make(chan string, 5)
	channel := ch
	copy := ch
	copy <- direct
	var assigned string
	assigned=ch
	assigned <- assigned
	relay(ch)
 namedRelay(ch: ch)
 defaultRelay()
	first := <-ch
	second := <-ch
	third := <-ch
	fourth := <-ch
	fifth := <-ch
 echo "$first"
 echo "$second"
 echo "$third"
	echo "$fourth"
	echo "$fifth"
 forged := "$ch"
 forged <- denied
}
main()
`
	out, err, status := genericMethodOracle(t, source)
	if out != "direct\nassigned\nfunction\nnamed\ndefault\n" || err != "bash++: forged is not a channel in this task group\n" || status != 2 {
		t.Fatalf("source contract changed: %q %q %d", out, err, status)
	}
	execute(t, compile(t, source))
}
