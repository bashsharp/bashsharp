//go:build gosource_testing_corpus

package interp_test

// Sprint: #118; Story: #56; Story-ID: 3ef468f4e831
// Explicit complete corpus gate. Every discovered Test root is invoked; this
// tag controls only the harness, never interpreter features or source selection.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/lower"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"testing"
	"time"
)

func TestGoSourceTestingErrorsCorpus(t *testing.T) {
	pins := map[string]string{
		"errors_test.go":  "7827d7e3ab7cc317a254ccf76158069b80c5b55b34ca45a3e6d799ad185f70ea",
		"example_test.go": "db75531e0575928a48a16d2c9769da59e719da091a31a4f03c3d5aeeca6be648",
		"join_test.go":    "09f9cc24acc7749d8f7197c6b90dc43223708c8f6412825e29ea3984bc6afdbc",
		"wrap_test.go":    "a098c0f752095df4614abf02925e52d2d35228e6ac272925dd4d9fd59f7e0cf6",
	}
	var sources []gosource.Source
	dir := filepath.Join(runtime.GOROOT(), "src", "errors")
	for name, digest := range pins {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", sha256.Sum256(data)) != digest {
			t.Fatalf("requires pinned original Go1.27 errors source: %s", path)
		}
		sources = append(sources, gosource.Source{Name: path, Data: data})
	}
	program, err := gosource.Load(sources, gosource.Options{Importer: lower.NewModuleImporter(dir)})
	if err != nil {
		t.Fatal(err)
	}
	if program.Package != "errors_test" || len(program.Sources) != 4 {
		t.Fatalf("wrong original package: %#v", program.Sources)
	}
	var out, errs bytes.Buffer
	runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	session, err := runner.LoadGoSourceTests(ctx, program)
	if err != nil {
		t.Fatalf("load original package: %v; %s", err, errs.String())
	}
	defer session.Close()
	tests, err := session.Tests()
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(tests)
	t.Logf("REGISTRATION %s", data)
	for _, test := range tests {
		name := test.Name
		errs.Reset()
		t.Run(name, func(t *testing.T) {
			defer func() {
				if value := recover(); value != nil {
					t.Errorf("interpreter panic: %v\n%s", value, debug.Stack())
				}
			}()
			if err := session.Run(ctx, name, t); err != nil {
				t.Fatalf("original body: %v; %s", err, errs.String())
			}
		})
	}
	for _, source := range sources {
		after, err := os.ReadFile(source.Name)
		if err != nil || !bytes.Equal(after, source.Data) {
			t.Fatalf("original source changed: %s", source.Name)
		}
	}
}
