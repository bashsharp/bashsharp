//go:build full

package lower_test

import "testing"

func TestRuntimeNativeABI(t *testing.T) {
	for _, src := range []string{
		`agentic { echo hi; }`,
		`func main() {
 ch := make(chan int, 1)
 ch <- 7
 value := <-ch
 println(value)
 close(ch)
}
main()
`,
		`agentic func action() { echo allowed }
agentic { action(); }
`,
		`agentic func action() { echo forbidden }
action()
echo after
`,
		`agentic func action() { echo denied }
func ordinary() { action(); }
agentic { ordinary(); }
`,
		`agentic func action() { echo allowed }
handle := action
agentic { handle(); }
`,
		`agentic func action() { echo forbidden }
handle := action
handle()
`,
		`agentic func action() { echo allowed }
func main() {
 agentic { defer action(); }
}
main()
`,
		`func main() {
 ch := make(chan int)
 go func() { ch <- 8; close(ch); }()
 value := <-ch
 println(value)
}
main()
`,
		`func leaf(ch) { ch <- nested; close(ch); }
func parent(ch) { go leaf(ch); }
func main() {
 ch := make(chan string)
 go parent(ch)
 for v := range ch { echo $v; }
}
main()
`,
		`func blocked(ch) { ch <- 1; }
func main() {
 ch := make(chan int)
 go blocked(ch)
}
main()
`,
		`func worker(ch) {
 ch <- 7
 close(ch)
}
func main() {
 ch := make(chan int)
 go worker(ch)
 for value := range ch { println(value) }
}
main()
`,
	} {
		t.Run(src, func(t *testing.T) { execute(t, compile(t, src)) })
	}
}

func TestRuntimeNativeTaskPanicStateRace(t *testing.T) {
	executeBuild(t, compile(t, `func worker(ch) {
 defer func() {
  payload := recover()
  ch <- 7
 }()
 panic("task")
}
func main() {
 ch := make(chan int, 2)
 go worker(ch)
 go worker(ch)
 first := <-ch
 second := <-ch
 sum := first + second
 println(sum)
}
main()
`), "-race")
}
