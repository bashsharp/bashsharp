//go:build full

package interp_test

// Sprint: #118; Story: #54; Story-ID: c3a60493cde9
import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func runGoSource(t *testing.T, name, source string) (string, string, error) {
	t.Helper()
	program, err := gosource.Parse(strings.NewReader(source), name+".go", gosource.Options{RunMain: true})
	if err != nil {
		t.Fatalf("gosource rejected the source: %v", err)
	}
	var out, errout bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	err = runner.Run(ctx, program.File)
	return out.String(), errout.String(), err
}

// Go's implicit field name for an embedded imported type is the *unqualified*
// type name, so `sync.Mutex` is selected as `.Mutex`. The runtime used to key
// the field under the qualified spelling, which is not a single selector and so
// could not be written at all.
func TestGoSourceEmbeddedImportedFieldName(t *testing.T) {
	for name, source := range map[string]string{
		"named_struct": `package main;import ("fmt";"sync");type box struct{n int;sync.Mutex};func main(){b:=box{};b.Mutex.Lock();b.n=5;b.Mutex.Unlock();fmt.Println(b.n)}`,
		"anon_struct":  `package main;import ("fmt";"sync");var g = struct{n int;sync.Mutex}{n:1};func main(){g.Mutex.Lock();g.n++;g.Mutex.Unlock();fmt.Println(g.n)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// The promoted spelling must reach the same embedded receiver the explicit one
// does, at every depth and through every pointer position Go allows.
func TestGoSourceEmbeddedImportedMethodPromotion(t *testing.T) {
	for name, source := range map[string]string{
		// The shape `solutions/webcrawler.go` uses: an anonymous struct at
		// package scope embedding sync.Mutex beside the map it guards.
		"anon_package_var":                `package main;import ("fmt";"sync");var fetched = struct{m map[string]int;sync.Mutex}{m:make(map[string]int)};func main(){fetched.Lock();fetched.m["a"]=1;fetched.Unlock();fetched.Lock();fmt.Println(fetched.m["a"]);fetched.Unlock()}`,
		"native_internal_promotion_depth": `package main;import("fmt";"bytes";"bufio";"strings");type box struct{*bufio.ReadWriter;*bytes.Buffer};func main(){b:=box{ReadWriter:bufio.NewReadWriter(bufio.NewReader(strings.NewReader("wrong\n")),bufio.NewWriter(bytes.NewBufferString(""))),Buffer:bytes.NewBufferString("right\n")};s,e:=b.ReadString('\n');fmt.Printf("%s%v\n",s,e)}`,
		"distinct_native_sets":            `package main;import("fmt";"sync";"bytes");type box struct{sync.Mutex;*bytes.Buffer};func main(){b:=box{Buffer:bytes.NewBufferString("a")};b.Lock();b.Write([]byte("b"));b.Mutex.Unlock();fmt.Println(b.String())}`,
		"unrelated_shallower":             `package main;import("fmt";"sync";"bytes");type inner struct{*bytes.Buffer};type box struct{sync.Mutex;inner};func main(){b:=box{inner:inner{Buffer:bytes.NewBufferString("a")}};b.Lock();b.Write([]byte("b"));b.Unlock();fmt.Println(b.String())}`,
		"pointer_embedded_value_root":     `package main;import("fmt";"sync";"bytes");type box struct{*sync.Mutex;*bytes.Buffer};func main(){b:=box{Mutex:new(sync.Mutex),Buffer:bytes.NewBufferString("a")};b.Lock();b.Buffer.WriteString("b");b.Unlock();fmt.Println(b.String())}`,
		"named_value":                     `package main;import ("fmt";"sync");type box struct{n int;sync.Mutex};func main(){b:=box{};b.Lock();b.n=5;b.Unlock();fmt.Println(b.n)}`,
		"pointer_root":                    `package main;import ("fmt";"sync");type box struct{n int;sync.Mutex};func main(){b:=&box{};b.Lock();b.n=7;b.Unlock();fmt.Println(b.n)}`,
		"depth_two":                       `package main;import ("fmt";"sync");type inner struct{sync.Mutex};type outer struct{inner;n int};func main(){o:=outer{};o.Lock();o.n=9;o.Unlock();fmt.Println(o.n)}`,
		"waitgroup":                       `package main;import ("fmt";"sync");type pool struct{sync.WaitGroup;n int};func main(){p:=&pool{};p.Add(1);go func(){p.Done()}();p.Wait();fmt.Println("joined")}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// Promotion must reach the *stored* mutex, not a copy of it, or the lock would
// guard nothing. Identity is checked without a second goroutine, so this does
// not depend on the separate goroutine-capture gap: the two spellings are
// interleaved, and Go panics on `Unlock` of an unlocked mutex, so a copied
// receiver fails outright instead of silently locking nothing. The counter then
// shows the lock is genuinely re-entered rather than a no-op.
func TestGoSourceEmbeddedImportedMethodReceiverIdentity(t *testing.T) {
	for name, source := range map[string]string{
		// Locked promoted, unlocked through the explicit embedded field.
		"promoted_lock_explicit_unlock": `package main;import ("fmt";"sync");var g = struct{n int;sync.Mutex}{};func main(){g.Lock();g.n++;g.Mutex.Unlock();fmt.Println(g.n)}`,
		// And the reverse, so neither spelling is the privileged one.
		"explicit_lock_promoted_unlock": `package main;import ("fmt";"sync");var g = struct{n int;sync.Mutex}{};func main(){g.Mutex.Lock();g.n++;g.Unlock();fmt.Println(g.n)}`,
		// Repeated lock/unlock cycles through the promoted spelling on the
		// webcrawler's own shape: an anonymous struct guarding a map.
		"repeated_cycles": `package main
import ("fmt";"sync")
var counter = struct{
	m map[string]int
	sync.Mutex
}{m: make(map[string]int)}
func main() {
	for j := 0; j < 400; j++ {
		counter.Lock()
		counter.m["hits"]++
		counter.Unlock()
	}
	counter.Lock()
	fmt.Println(counter.m["hits"])
	counter.Unlock()
}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// A promoted receiver still has to be one the dependency can be handed. When
// the embedded imported type sits inside a *local* struct field, a local type
// that references it (holder → box, where box embeds sync.Mutex) was once
// emitted into the helper with the unmaterialisable box still named, so the
// worker failed to build with "undefined: box". Keeping the materialised set
// dependency-closed (S153.4) now drops the referencing type instead of
// emitting an uncompilable declaration, and the promoted mutex reaches the
// stored receiver through the ordinary path. Promoted and explicit spellings
// agree with native Go across all three modes.
func TestGoSourceEmbeddedImportedMethodLocalCodec(t *testing.T) {
	for name, source := range map[string]string{
		"promoted": `package main;import ("fmt";"sync");type box struct{n int;sync.Mutex};type holder struct{b box};func main(){h:=holder{};h.b.Lock();h.b.n=3;h.b.Unlock();fmt.Println(h.b.n)}`,
		"explicit": `package main;import ("fmt";"sync");type box struct{n int;sync.Mutex};type holder struct{b box};func main(){h:=holder{};h.b.Mutex.Lock();h.b.n=3;h.b.Mutex.Unlock();fmt.Println(h.b.n)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

// Promotion is a last resort behind the ordinary Go rules, not a shortcut past
// them. A name that the local type itself provides must keep winning, and two
// candidates at one depth stay ambiguous rather than silently picking the
// imported one.
func TestGoSourceEmbeddedImportedMethodShadowing(t *testing.T) {
	// A shallower local method outranks the embedded imported one.
	t.Run("local_method_shadows", func(t *testing.T) {
		source := `package main;import ("fmt";"sync");type box struct{sync.Mutex};func (b *box) Lock(){fmt.Println("local")};func main(){b:=&box{};b.Lock()}`
		out, errout, err := runGoSource(t, "shadow", source)
		if err != nil {
			t.Fatalf("err=%v errout=%q", err, errout)
		}
		if strings.TrimSpace(out) != "local" {
			t.Fatalf("embedded imported method outranked the local one: %q", out)
		}
	})
	// A field at the same depth as the embedded type shadows it too.
	t.Run("local_field_shadows", func(t *testing.T) {
		source := `package main;import ("fmt";"sync");type box struct{Lock int;sync.Mutex};func main(){b:=box{Lock:4};fmt.Println(b.Lock)}`
		out, errout, err := runGoSource(t, "fieldshadow", source)
		if err != nil {
			t.Fatalf("err=%v errout=%q", err, errout)
		}
		if strings.TrimSpace(out) != "4" {
			t.Fatalf("field shadowing lost: %q", out)
		}
	})
	// Two embedded imported types at depth 1 both provide Lock; Go rejects the
	// selector, and so must the runtime rather than choosing one.
	t.Run("ambiguous_same_depth", func(t *testing.T) {
		source := `package main;import "sync";type box struct{sync.Mutex;sync.RWMutex};func main(){b:=box{};b.Lock()}`
		program, parseErr := gosource.Parse(strings.NewReader(source), "ambiguous.go", gosource.Options{RunMain: true})
		if parseErr != nil {
			if !strings.Contains(parseErr.Error(), "ambiguous selector") {
				t.Fatalf("rejected for the wrong reason: %v", parseErr)
			}
			return
		}
		// Rejection may come from the type checker or the runtime; what matters
		// is that one embedded mutex is not quietly chosen over the other.
		var out, errout bytes.Buffer
		runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errout))
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := runner.Run(ctx, program.File); err == nil {
			t.Fatalf("accepted an ambiguous promoted selector: %q", out.String())
		}
	})
}
