//go:build full

package interp

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS374FastStdlibEligibility(t *testing.T) {
	req := bashPPEvalRequest{Imports: map[string]string{"conv": "strconv", "rng": "math/rand"}}
	q := bashPPBridgeRequest{Op: "call", Selector: "conv.Itoa", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "41"}}}
	got, handled, err := goSourceFastStdlibCall(context.Background(), req, q)
	if err != nil || !handled || len(got) != 1 || got[0].stringText() != "41" {
		t.Fatalf("Itoa direct call: %#v, %v, %v", got, handled, err)
	}
	q.Args[0].Origin = 1
	if _, handled, _ := goSourceFastStdlibCall(context.Background(), req, q); handled {
		t.Fatal("value with interpreter identity took direct path")
	}
	for _, source := range []string{
		`package main; import "math/rand"; func main(){ _=rand.Read }`,
		`package main; import "math/rand"; func main(){ var b [4]byte; rand.Read(b[:]) }`,
		`package main; import "math/rand"; func main(){ _=rand.Intn(2); rand:=struct{Intn func(int)int}{}; _=rand.Intn }`,
	} {
		path := filepath.Join(t.TempDir(), "main.go")
		if err := os.WriteFile(path, []byte(source), 0600); err != nil {
			t.Fatal(err)
		}
		if fastRandSourceProof(bashPPEvalRequest{SourceFile: path, Imports: map[string]string{"rand": "math/rand"}}, "rand") {
			t.Fatal("uncovered random operation claimed interpreter ownership")
		}
	}
}

func TestS374FastStdlibPureFamilies(t *testing.T) {
	cases := []struct {
		path, selector string
		args           []bashPPBridgeValue
	}{
		{"math", "m.Sin", []bashPPBridgeValue{{Kind: "float", Type: "float64", Text: "0"}}},
		{"math/bits", "b.OnesCount", []bashPPBridgeValue{{Kind: "uint", Type: "uint", Text: "7"}}},
		{"strings", "s.Count", []bashPPBridgeValue{{Kind: "string", Type: "string", Text: "banana"}, {Kind: "string", Type: "string", Text: "an"}}},
		{"bytes", "by.Count", []bashPPBridgeValue{{Kind: "slice", Type: "[]uint8", Length: 1, Capacity: 1, Elements: []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "97"}}}, {Kind: "slice", Type: "[]uint8", Length: 1, Capacity: 1, Elements: []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "97"}}}}},
		{"unicode", "u.IsLetter", []bashPPBridgeValue{{Kind: "int", Type: "int32", Text: "65"}}},
		{"unicode/utf8", "u8.RuneCountInString", []bashPPBridgeValue{{Kind: "string", Type: "string", Text: "café"}}},
		{"strconv", "c.AppendInt", []bashPPBridgeValue{{Kind: "slice", Type: "[]uint8", Length: 1, Capacity: 1, Elements: []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "120"}}}, {Kind: "int", Type: "int64", Text: "31"}, {Kind: "int", Type: "int", Text: "10"}}},
		{"fmt", "f.Sprintf", []bashPPBridgeValue{{Kind: "string", Type: "string", Text: "%d"}, {Kind: "int", Type: "int", Text: "42"}}},
	}
	for _, tc := range cases {
		t.Run(tc.selector, func(t *testing.T) {
			alias, _, _ := strings.Cut(tc.selector, ".")
			req := bashPPEvalRequest{Imports: map[string]string{alias: tc.path}}
			values, handled, err := goSourceFastStdlibCall(context.Background(), req, bashPPBridgeRequest{Op: "call", Selector: tc.selector, Args: tc.args})
			if err != nil || !handled || len(values) == 0 {
				t.Fatalf("values=%#v handled=%v err=%v", values, handled, err)
			}
		})
	}
}

func TestS374FastStdlibAddedWorkerParity(t *testing.T) {
	source := `package main
import ("math"; "math/bits"; "strings"; "bytes"; "unicode"; "unicode/utf8"; "strconv"; "fmt")
func main() { println("capture"); _=math.Sin(0); _=bits.OnesCount(1); _=strings.Count("a","a"); _=bytes.Count([]byte("a"),[]byte("a")); _=unicode.IsLetter('A'); _=utf8.RuneCountInString("a"); _=strconv.AppendInt(nil,1,10); _=fmt.Sprintf("%d",1) }`
	aliases := map[string]string{"math": "math", "math/bits": "bits", "strings": "strings", "bytes": "bytes", "unicode": "unicode", "unicode/utf8": "utf8", "strconv": "strconv", "fmt": "fmt"}
	for path, symbols := range fastPureSymbols {
		for name := range symbols {
			source = strings.Replace(source, "println(\"capture\");", "println(\"capture\"); _="+aliases[path]+"."+name+";", 1)
		}
	}
	for name := range fastStrconvSymbols {
		source = strings.Replace(source, "println(\"capture\");", "println(\"capture\"); _=strconv."+name+";", 1)
	}
	var runner *Runner
	var probeErr error
	called := false
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "capture") || called {
			return len(p), nil
		}
		called = true
		req, err := runner.bashPPEvalRequest()
		if err != nil {
			probeErr = err
			return len(p), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		families := map[string]map[string]reflect.Value{"strconv": fastStrconvSymbols}
		for path, symbols := range fastPureSymbols {
			families[path] = symbols
		}
		for path, symbols := range families {
			for name, fn := range symbols {
				alias := aliases[path]
				if alias == "" {
					continue
				}
				q := bashPPBridgeRequest{Op: "call", Selector: alias + "." + name}
				sig := fn.Type()
				for i := 0; i < sig.NumIn(); i++ {
					if sig.IsVariadic() && i == sig.NumIn()-1 {
						break
					}
					q.Args = append(q.Args, s374PureArg(sig.In(i)))
				}
				if name == "AppendFloat" || name == "FormatFloat" {
					q.Args[len(q.Args)-1].Text = "64"
				}
				if name == "FormatComplex" {
					q.Args[len(q.Args)-1].Text = "128"
				}
				if path == "fmt" {
					if name == "Sprintf" {
						q.Args[0] = bashPPBridgeString("%d %s")
					}
					q.Args = append(q.Args, bashPPBridgeValue{Kind: "int", Text: "7"}, bashPPBridgeValue{Kind: "string", Text: "ok"})
				}
				fast, handled, err := goSourceFastStdlibCall(ctx, req, q)
				if err != nil || !handled {
					probeErr = fmt.Errorf("%s fast handled=%v err=%v", q.Selector, handled, err)
					return len(p), nil
				}
				worker, err := req.Bridge.request(ctx, req, q)
				if err != nil {
					probeErr = fmt.Errorf("%s worker: %w", q.Selector, err)
					return len(p), nil
				}
				if len(fast) != len(worker) {
					probeErr = fmt.Errorf("%s length fast=%#v worker=%#v", q.Selector, fast, worker)
					return len(p), nil
				}
				for i := range fast {
					match, compareErr := s374SamePureResult(ctx, req, fast[i], worker[i])
					if compareErr != nil || !match {
						probeErr = fmt.Errorf("%s result %d mismatch: %v (fast kind=%s type=%s, worker kind=%s type=%s)", q.Selector, i, compareErr, fast[i].Kind, fast[i].Type, worker[i].Kind, worker[i].Type)
						return len(p), nil
					}
				}
			}
		}
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runner.Dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatal(err)
	}
	if !called || probeErr != nil {
		t.Fatalf("called=%v err=%v", called, probeErr)
	}
}

func s374PureArg(typ reflect.Type) bashPPBridgeValue {
	if typ == reflect.TypeOf([]byte(nil)) {
		return bashPPBridgeValue{Kind: "slice", Type: "[]uint8", Length: 2, Capacity: 2, Elements: []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "97"}, {Kind: "uint", Type: "uint8", Text: "98"}}}
	}
	switch typ.Kind() {
	case reflect.String:
		return bashPPBridgeString("ab")
	case reflect.Bool:
		return bashPPBridgeValue{Kind: "bool", Type: typ.String(), Text: "true"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return bashPPBridgeValue{Kind: "int", Type: typ.String(), Text: "2"}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		value := "2"
		if typ.Kind() == reflect.Uint8 {
			value = "103"
		}
		return bashPPBridgeValue{Kind: "uint", Type: typ.String(), Text: value}
	case reflect.Float32, reflect.Float64:
		return bashPPBridgeValue{Kind: "float", Type: typ.String(), Text: "0.5"}
	case reflect.Complex64, reflect.Complex128:
		return bashPPBridgeValue{Kind: "complex", Type: typ.String(), Text: "(0.5+0.25i)"}
	}
	panic("unsupported pure argument " + typ.String())
}

func s374SamePureValue(a, b bashPPBridgeValue) bool {
	if a.Kind != b.Kind || a.Type != b.Type {
		return false
	}
	if a.Kind == "slice" {
		if len(a.Elements) != len(b.Elements) {
			return false
		}
		for i := range a.Elements {
			if !s374SamePureValue(a.Elements[i], b.Elements[i]) {
				return false
			}
		}
		return true
	}
	if a.Kind == "string" {
		return a.stringText() == b.stringText()
	}
	return a.Text == b.Text
}

func s374SamePureResult(ctx context.Context, req bashPPEvalRequest, direct, worker bashPPBridgeValue) (bool, error) {
	if direct.Kind != "slice" || worker.Kind != "handle" {
		return s374SamePureValue(direct, worker), nil
	}
	if direct.Type != worker.Type {
		return false, nil
	}
	length, err := req.Bridge.request(ctx, req, bashPPBridgeRequest{Op: "len", Receiver: &worker})
	if err != nil {
		return false, err
	}
	if len(length) != 1 || length[0].Text != strconv.Itoa(direct.Length) {
		return false, nil
	}
	capacity, err := req.Bridge.request(ctx, req, bashPPBridgeRequest{Op: "cap", Receiver: &worker})
	if err != nil {
		return false, err
	}
	if len(capacity) != 1 || capacity[0].Text != strconv.Itoa(direct.Capacity) {
		return false, nil
	}
	for i, elem := range direct.Elements[:direct.Length] {
		got, err := req.Bridge.request(ctx, req, bashPPBridgeRequest{Op: "index", Receiver: &worker, Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: strconv.Itoa(i)}}})
		if err != nil {
			return false, err
		}
		if len(got) != 1 || !s374SamePureValue(elem, got[0]) {
			return false, nil
		}
	}
	return true, nil
}

func TestS374FastStdlibRealSprintf(t *testing.T) {
	source := `package main; import "fmt"; type Fancy int; func main(){for i:=0;i<3;i++{if got:=fmt.Sprintf("%d_%s",i,"x"); got!=[]string{"0_x","1_x","2_x"}[i] {panic(got)}}; if got:=fmt.Sprintf("%T",Fancy(7)); got!="main.Fancy" {panic(got)}}`
	path := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, stderr strings.Builder
	r, err := New(Lang(syntax.LangBashPP), Dir(filepath.Dir(path)), StdIO(nil, &out, &stderr))
	if err != nil {
		t.Fatal(err)
	}
	var calls []bashPPBridgeRequest
	old := bashPPNativeRequestTrace
	bashPPNativeRequestTrace = func(q bashPPBridgeRequest) {
		if q.Selector == "fmt.Sprintf" {
			calls = append(calls, q)
		}
	}
	defer func() { bashPPNativeRequestTrace = old }()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Run(ctx, p.File); err != nil {
		t.Fatalf("%v stderr=%q", err, stderr.String())
	}
	if len(calls) != 1 {
		t.Fatalf("%d fmt.Sprintf worker calls; calls=%#v", len(calls), calls)
	}
}

func TestS374FastStdlibPureFallback(t *testing.T) {
	req := bashPPEvalRequest{Imports: map[string]string{"f": "fmt", "b": "bytes"}}
	plain := bashPPBridgeRequest{Op: "call", Selector: "f.Sprintf", Args: []bashPPBridgeValue{bashPPBridgeString("%v"), {Kind: "int", Type: "int", Text: "7"}}}
	for name, change := range map[string]func(*bashPPBridgeRequest){
		"named scalar":       func(q *bashPPBridgeRequest) { q.Args[1].Type = "main.Custom" },
		"interface identity": func(q *bashPPBridgeRequest) { q.Args[1].Interface = "interface{}" },
		"handle":             func(q *bashPPBridgeRequest) { q.Args[1].Handle = 1 },
		"spread":             func(q *bashPPBridgeRequest) { q.Spread = true },
		"receiver":           func(q *bashPPBridgeRequest) { q.Receiver = &bashPPBridgeValue{Kind: "handle", Handle: 1} },
	} {
		t.Run(name, func(t *testing.T) {
			q := plain
			q.Args = append([]bashPPBridgeValue(nil), plain.Args...)
			change(&q)
			if _, handled, _ := goSourceFastStdlibCall(context.Background(), req, q); handled {
				t.Fatal("ineligible formatting call used direct path")
			}
		})
	}
	q := bashPPBridgeRequest{Op: "call", Selector: "b.Count", Args: []bashPPBridgeValue{{Kind: "slice", Type: "[]uint8", Length: 1, Capacity: 1, Storage: 1, Elements: []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "1"}}}, {Kind: "slice", Type: "[]uint8", Length: 1, Capacity: 1, Elements: []bashPPBridgeValue{{Kind: "uint", Type: "uint8", Text: "1"}}}}}
	if _, handled, _ := goSourceFastStdlibCall(context.Background(), req, q); handled {
		t.Fatal("byte slice with storage identity used direct path")
	}
}

func TestS374FastStdlibWorkerParity(t *testing.T) {
	t.Setenv("GODEBUG", "randautoseed=0,randseednop=0")
	source := `package main
import "strconv"
import "math/rand"
func main(){println("capture"); _=strconv.Itoa(1); _=strconv.FormatInt(1,16); _=strconv.Quote("x"); rand.Seed(17); _=rand.Intn(10); _=rand.Int63()}`
	var runner *Runner
	var probeErr error
	called := false
	writer := callbackProbeWriter(func(p []byte) (int, error) {
		if !strings.Contains(string(p), "capture") || called {
			return len(p), nil
		}
		called = true
		req, err := runner.bashPPEvalRequest()
		if err != nil {
			probeErr = err
			return len(p), nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, q := range []bashPPBridgeRequest{
			{Op: "call", Selector: "strconv.Itoa", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "-73"}}},
			{Op: "call", Selector: "strconv.FormatInt", Args: []bashPPBridgeValue{{Kind: "int", Type: "int64", Text: "-73"}, {Kind: "int", Type: "int", Text: "16"}}},
			{Op: "call", Selector: "strconv.Quote", Args: []bashPPBridgeValue{{Kind: "string", Type: "string", Text: "a\n"}}},
		} {
			fast, handled, err := goSourceFastStdlibCall(ctx, req, q)
			if err != nil || !handled {
				probeErr = fmt.Errorf("fast %s: handled=%v err=%v", q.Selector, handled, err)
				return len(p), nil
			}
			worker, err := req.Bridge.request(ctx, req, q)
			if err != nil {
				probeErr = err
				return len(p), nil
			}
			if len(fast) != len(worker) || fast[0].Kind != worker[0].Kind || fast[0].Type != worker[0].Type || fast[0].Text != worker[0].Text || fast[0].stringText() != worker[0].stringText() {
				probeErr = fmt.Errorf("%s: fast=%#v worker=%#v", q.Selector, fast, worker)
				return len(p), nil
			}
		}
		seed := bashPPBridgeRequest{Op: "call", Selector: "rand.Seed", Args: []bashPPBridgeValue{{Kind: "int", Type: "int64", Text: "23"}}}
		for _, q := range []bashPPBridgeRequest{{Op: "call", Selector: "rand.Intn", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "1000000"}}}, seed, {Op: "call", Selector: "rand.Intn", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "1000000"}}}, {Op: "call", Selector: "rand.Int63"}} {
			fast, handled, err := goSourceFastStdlibCall(ctx, req, q)
			if err != nil || !handled {
				probeErr = fmt.Errorf("fast %s: handled=%v err=%v", q.Selector, handled, err)
				return len(p), nil
			}
			worker, err := req.Bridge.request(ctx, req, q)
			if err != nil {
				probeErr = err
				return len(p), nil
			}
			if len(fast) != len(worker) || len(fast) > 0 && (fast[0].Kind != worker[0].Kind || fast[0].Type != worker[0].Type || fast[0].Text != worker[0].Text) {
				probeErr = fmt.Errorf("%s: fast=%#v worker=%#v", q.Selector, fast, worker)
				return len(p), nil
			}
		}
		bad := bashPPBridgeRequest{Op: "call", Selector: "rand.Intn", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "-1"}}}
		_, handled, directErr := goSourceFastStdlibCall(ctx, req, bad)
		_, workerErr := req.Bridge.request(ctx, req, bad)
		if handled || directErr != nil || workerErr == nil {
			probeErr = fmt.Errorf("panic parity: handled=%v direct=%v worker=%v", handled, directErr, workerErr)
		}
		return len(p), nil
	})
	var err error
	runner, err = New(Lang(syntax.LangBashPP), Dir(t.TempDir()), StdIO(nil, io.Discard, writer))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runner.Dir, "main.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := runner.Run(ctx, program.File); err != nil {
		t.Fatal(err)
	}
	if !called || probeErr != nil {
		t.Fatalf("probe called=%v error=%v", called, probeErr)
	}
}

func BenchmarkS374StdlibCall(b *testing.B) {
	for _, symbol := range []string{"strconv.Itoa", "math/rand.Intn"} {
		for _, mode := range []string{"worker", "direct"} {
			b.Run(symbol+"/"+mode, func(b *testing.B) {
				b.StopTimer()
				source := `package main
import "strconv"
func main(){println("capture"); _=strconv.Itoa(1)}`
				q := bashPPBridgeRequest{Op: "call", Selector: "strconv.Itoa", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "41"}}}
				if symbol == "math/rand.Intn" {
					source = `package main
import "math/rand"
func main(){println("capture"); _=rand.Intn(1000000)}`
					q = bashPPBridgeRequest{Op: "call", Selector: "rand.Intn", Args: []bashPPBridgeValue{{Kind: "int", Type: "int", Text: "1000000"}}}
				}
				var runner *Runner
				var probeErr error
				called := false
				writer := callbackProbeWriter(func(p []byte) (int, error) {
					if !strings.Contains(string(p), "capture") || called {
						return len(p), nil
					}
					called = true
					req, err := runner.bashPPEvalRequest()
					if err != nil {
						probeErr = err
						return len(p), nil
					}
					req.disableFastStdlib = mode == "worker"
					ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
					defer cancel()
					if _, err := runner.bashPPNativeRequest(ctx, req, q); err != nil {
						probeErr = err
						return len(p), nil
					}
					b.ReportAllocs()
					b.ResetTimer()
					b.StartTimer()
					for i := 0; i < b.N; i++ {
						values, err := runner.bashPPNativeRequest(ctx, req, q)
						valid := len(values) == 1
						if valid && symbol == "strconv.Itoa" {
							valid = values[0].stringText() == "41"
						}
						if valid && symbol == "math/rand.Intn" {
							n, parseErr := strconv.Atoi(values[0].Text)
							valid = parseErr == nil && n >= 0 && n < 1000000
						}
						if err != nil || !valid {
							probeErr = fmt.Errorf("call %d: %v, %#v", i, err, values)
							break
						}
					}
					b.StopTimer()
					return len(p), nil
				})
				var err error
				runner, err = New(Lang(syntax.LangBashPP), Dir(b.TempDir()), StdIO(nil, io.Discard, writer))
				if err != nil {
					b.Fatal(err)
				}
				path := filepath.Join(runner.Dir, "main.go")
				if err := os.WriteFile(path, []byte(source), 0600); err != nil {
					b.Fatal(err)
				}
				program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
				if err != nil {
					b.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				if err := runner.Run(ctx, program.File); err != nil {
					b.Fatal(err)
				}
				if !called || probeErr != nil {
					b.Fatalf("probe called=%v error=%v", called, probeErr)
				}
			})
		}
	}
}
