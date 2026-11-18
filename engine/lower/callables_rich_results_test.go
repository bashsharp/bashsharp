package lower_test

import "testing"

func TestCompiledNamedRichResults(t *testing.T) {
	const source = `type Box struct { N int }
type Ch int
func rich() (ptr *Box, value Box, pipe Ch) {
 p := new(Box)
 p.N = 5
 b := Box{N: 6}
 ch := make(chan int, 1)
 ch <- 7
 return p, b, ch
}
func main() {
 var p *Box
 var b Box
 var ch Ch
 p, b, ch = rich()
 pv := *p
 got := <-ch
 printf '%s:%s:%s' pv.N b.N "$got"
}
main()
`
	execute(t, compile(t, source))
}
