//go:build full

package interp_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/gosource"
	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

func TestS243ComplexUnchangedRoots(t *testing.T) {
	for _, files := range [][]string{{"cmplxdivide.go", "cmplxdivide1.go"}, {"map.go"}, {"fixedbugs/issue6866.go"}} {
		t.Run(files[0], func(t *testing.T) {
			var sources []gosource.Source
			for _, name := range files {
				path := filepath.Join(runtime.GOROOT(), "test", name)
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				sources = append(sources, gosource.Source{Name: path, Data: data})
			}
			program, err := gosource.Load(sources, gosource.Options{RunMain: true})
			if err != nil {
				t.Fatal(err)
			}
			var out, stderr bytes.Buffer
			runner, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(t.TempDir()), interp.StdIO(nil, &out, &stderr))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()
			if err := runner.Run(ctx, program.File); err != nil {
				t.Fatalf("run: %v\nstdout=%s\nstderr=%s", err, out.String(), stderr.String())
			}
			if out.Len() != 0 || stderr.Len() != 0 {
				t.Fatalf("stdout=%s stderr=%s", out.String(), stderr.String())
			}
		})
	}
}

func TestS243ComplexSignedZeroBoundaries(t *testing.T) {
	source := `package main
import "math"
func id(z complex128) complex128 {
 if !math.Signbit(imag(z)) || math.Signbit(real(z)) {panic("argument imaginary zero sign")}
 return z
}
func id64(z complex64) complex64 {
 if !math.Signbit(float64(real(z))) || math.Signbit(float64(imag(z))) {panic("argument real zero sign")}
 return z
}
func main(){
 neg:=math.Copysign(0,-1)
 z:=id(complex(0.0,neg))
 q:=id64(complex(float32(neg),float32(0)))
 if !math.Signbit(imag(z)) || math.Signbit(real(z)) {panic("result imaginary zero sign")}
 if !math.Signbit(float64(real(q))) || math.Signbit(float64(imag(q))) {panic("result real zero sign")}
 list:=[]complex128{z,complex128(q)}
 if !math.Signbit(imag(list[0])) || math.Signbit(real(list[0])) {panic("slice imaginary zero sign")}
 if !math.Signbit(real(list[1])) || math.Signbit(imag(list[1])) {panic("slice real zero sign")}
 array:=[1]complex64{q}
 if !math.Signbit(float64(real(array[0]))) || math.Signbit(float64(imag(array[0]))) {panic("array zero signs")}
 m:=map[complex128]int{z:1}
 if m[complex(0.0,0.0)]!=1||len(m)!=1 {panic("signed zero key equality")}
 n:=complex(math.NaN(),0.0)
 m[n]=2
 if _,ok:=m[n];ok{panic("NaN key equated")}
 if z!=complex(0.0,0.0) || n==n {panic("complex equality")}
}`
	_, stderr, err := runGoSource(t, "s243-complex-signedzero", source)
	if err != nil || stderr != "" {
		t.Fatalf("run: %v stderr=%s", err, stderr)
	}
}
