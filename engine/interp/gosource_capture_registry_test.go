//go:build full

package interp_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
)

func TestGoSourceCaptureRegistryThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"reassigned_after_launch_before_receive": `package main;import "fmt";func main(){a,b:=0,0;f:=func(){a++};g:=func(){b++};ready:=make(chan struct{});done:=make(chan struct{});go func(){<-ready;f();close(done)}();f=g;close(ready);<-done;fmt.Println(a,b)}`,
		"concurrent_function_cell_under_mutex":   `package main;import("fmt";"sync");func main(){a,b:=0,0;f:=func(){a++};g:=func(){b++};var mu sync.Mutex;var wg sync.WaitGroup;wg.Go(func(){mu.Lock();f=g;mu.Unlock()});for range 8{wg.Go(func(){mu.Lock();f();mu.Unlock()})};wg.Wait();fmt.Println(a+b)}`,
		"computed_callee_once":                   `package main;import("fmt";"sync");func main(){n,x:=0,0;var wg sync.WaitGroup;pick:=func()func(){n++;return func(){x++;wg.Done()}};wg.Add(1);go pick()();wg.Wait();fmt.Println(n,x)}`,
		"local_variable_shadows_import":          `package main;import("fmt";"sync");func show(n int){fmt.Println(n)};func main(){var wg sync.WaitGroup;fmt:=0;wg.Go(func(){fmt++});wg.Wait();show(fmt)}`,
		"reassigned_closure_after_wait":          `package main;import("fmt";"sync");func main(){a,b:=0,0;f:=func(){a++};var wg sync.WaitGroup;wg.Go(func(){f()});wg.Wait();f=func(){b++};wg.Go(func(){f()});wg.Wait();fmt.Println(a,b)}`,
		"package_function_global":                `package main;import("fmt";"sync");var n int;func inc(){n++};func main(){var wg sync.WaitGroup;wg.Go(func(){inc()});wg.Wait();fmt.Println(n)}`,
	} {
		t.Run(name, func(t *testing.T) { typedSendThreeModes(t, source) })
	}
}

func TestGoSourceCaptureOriginalMutexesThreeModes(t *testing.T) {
	source, err := os.ReadFile("testdata/gosource-task-capture/mutexes.go.txt")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(source)) != "288acf44044d532f12ead0d173a521d7a67f272f9d4ce611c90dcdcafe0221f7" {
		t.Fatal("original bytes changed")
	}
	typedSendThreeModes(t, string(source))
}
