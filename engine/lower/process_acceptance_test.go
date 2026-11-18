//go:build full

package lower_test

import "testing"

func TestProcessLinesParity(t *testing.T) {
	for _, tc := range []struct{ name, source, want string }{
		{"completed", `func main() {
	r, err := run("printf", 'a\n\nb')
	for line := range r.Lines() { printf '[%s]' "$line" }
	printf ' status=%s err=[%s]\n' r.Status "$err"
}
main()
`, "[a][][b] status=0 err=[]\n"},
		{"live", `func main() {
	p, err := start("printf", 'a\n\nb')
	for line := range p.Lines() { printf '[%s]' "$line" }
	status, err := p.Wait()
	echo " status=$status err=[$err]"
}
main()
`, "[a][][b] status=0 err=[]\n"},
		{"nonzero exact once", `func main() {
	p, err := start("false")
	a, e1 := p.Wait()
	b, e2 := p.Wait()
	p.Close()
	echo "$a/$b [$e1][$e2]"
}
main()
`, "1/1 [][]\n"},
		{"command status", `func main() {
	p, err := start("false")
	p.Wait()
	echo "wait=$?"
	p.Close()
	echo "close=$?"
}
main()
`, "wait=1\nclose=0\n"},
		{"completed nonzero", `func main() {
	r, err := run("false")
	for line := range r.Lines() { echo unexpected }
	printf 'status=%s err=[%s]\n' r.Status "$err"
}
main()
`, "status=1 err=[]\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectForeignOutcomes(t, tc.source, foreignOutcome{stdout: tc.want})
		})
	}
}
