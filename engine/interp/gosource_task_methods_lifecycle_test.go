//go:build full

package interp_test

import (
	"bytes"
	"context"
	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGoSourceTaskMethodReset(t *testing.T) {
	source := `package main
import "sync"
type Counter struct{mu sync.Mutex;n int}
func(c *Counter)Inc(done chan int){c.mu.Lock();c.n++;c.mu.Unlock();done<-1}
func main(){c:=Counter{};done:=make(chan int);go c.Inc(done);<-done;println(c.n)}`
	dir := t.TempDir()
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	program, err := gosource.Parse(strings.NewReader(source), path, gosource.Options{RunMain: true})
	if err != nil {
		t.Fatal(err)
	}
	var out, errs bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.StdIO(nil, &out, &errs))
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 3; run++ {
		r.Reset()
		out.Reset()
		errs.Reset()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		err = r.Run(ctx, program.File)
		cancel()
		if err != nil || out.String() != "" || errs.String() != "1\n" {
			t.Fatalf("run%d: %v stdout=%q stderr=%q", run, err, out.String(), errs.String())
		}
	}
}
