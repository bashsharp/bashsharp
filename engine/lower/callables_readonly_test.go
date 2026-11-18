//go:build full

package lower_test

import "testing"

func TestReadonlyNativeDispatch(t *testing.T) {
	for name, source := range map[string]string{
		"root": `cfg := map[string]int{"port":80}
readonly cfg
cfg = map[string]int{"port":443}
`,
		"map": `cfg := map[string]map[string]int{"nested":{"port":80}}
readonly cfg
cfg["nested"]["port"] = 443
`,
		"alias": `cfg := map[string][]int{"ports":{80,443}}
alias := cfg
readonly cfg
alias["ports"][0] = 8080
`,
		"field": `type Config struct { Name string }
cfg := Config{Name:"prod"}
readonly cfg
cfg.Name = "dev"
`,
		"pointer": `func main() {
 x := 1
 p := &x
 readonly x
 *p = 2
}
main()
`,
		"clear": `func main() {
 m := map[string]int{"x":1}
 readonly m
 clear(m)
}
main()
`,
	} {
		t.Run(name, func(t *testing.T) { execute(t, compile(t, source)) })
	}
}
