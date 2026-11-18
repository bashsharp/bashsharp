//go:build full

package lower_test

import "testing"

func TestChannelKeywordWord(t *testing.T) {
	execute(t, compile(t, `func main() {
 ch := make(chan string, 1)
 ch <- default
 value := <-ch
 println(value)
}
main()
`))
}
