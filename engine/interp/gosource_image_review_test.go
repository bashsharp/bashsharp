//go:build full

package interp_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bashsharp/bashsharp/engine/interp"
	"github.com/bashsharp/bashsharp/engine/syntax"
)

const imageReviewMethods = `
func(m *Picture)ColorModel()color.Model{return color.RGBAModel}
func(m *Picture)Bounds()image.Rectangle{return image.Rect(0,0,2,2)}
`

func TestGoSourceImageCallbackReviewThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"nested_native_return_panic": `package main
import("fmt";"strings")
func bad()string{return strings.FieldsFunc("x",func(rune)bool{panic("nested panic")})[0]}
func main(){defer func(){fmt.Println(recover())}();fmt.Println(bad());println("after")}`,
		"pointer_state": `package main
import("fmt";"bytes";"image";"image/color";"image/png")
type Picture struct{Calls int}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{m.Calls++;return color.RGBA{uint8(x),uint8(y),17,255}}
func main(){m:=Picture{};out:=bytes.NewBufferString("");err:=png.Encode(out,&m);fmt.Println(m.Calls,out.Len(),err)}`,
		"positional_effect_order": `package main
import("fmt";"bytes";"image";"image/color";"image/png")
type Picture struct{}
var calls int
func next()uint8{calls++;return uint8(calls)}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{return color.RGBA{next(),next(),next(),next()}}
func main(){m:=Picture{};out:=bytes.NewBufferString("");err:=png.Encode(out,&m);fmt.Printf("%d %x %v\n",calls,out.Bytes(),err)}`,
		"original_panic": `package main
import("fmt";"bytes";"image";"image/color";"image/png")
type Picture struct{}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{panic("at panic")}
func main(){defer func(){fmt.Println(recover())}();out:=bytes.NewBufferString("");png.Encode(out,&Picture{});println("after")}`,
	} {
		t.Run(name, func(t *testing.T) { callbackTourThreeModes(t, source) })
	}
}

// A callback is not itself a result boundary. Imported composites used as
// locals must be fully materialized so their methods and fields keep native Go
// behavior before a separate value is returned to the dependency.
func TestGoSourceImageCallbackLocalCompositeBehaviorThreeModes(t *testing.T) {
	source := `package main
import("fmt";"bytes";"image";"image/color";"image/png")
type Picture struct{}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{
	r:=image.Rect(1,2,4,6)
	if r.Dx()!=3{panic("local method")}
	r.Max.X=9
	if r.Dx()!=8||r.Max.X!=9{panic("local field")}
	return color.RGBA{uint8(r.Dx()),uint8(r.Max.X),17,255}
}
func main(){out:=bytes.NewBufferString("");err:=png.Encode(out,&Picture{});fmt.Println(out.Len()>0,err)}`
	callbackTourThreeModes(t, source)
}

func TestGoSourceImageCallbackCancelReset(t *testing.T) {
	dir := callbackTourModule(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output := &callbackCancelWriter{cancel: cancel}
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, output, output))
	if err != nil {
		t.Fatal(err)
	}
	source := `package main
import("bytes";"image";"image/color";"image/png";"time")
type Picture struct{}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{println("callback-ready");time.Sleep(time.Minute);return color.RGBA{1,2,3,255}}
func main(){out:=bytes.NewBufferString("");png.Encode(out,&Picture{});println("after")}`
	if err = r.Run(ctx, callbackTourLoad(t, dir, source).File); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v %q", err, output.String())
	}
	if strings.Contains(output.String(), "after") {
		t.Fatal("continued after image cancellation")
	}
	r.Reset()
	output.Reset()
	source = `package main
import("fmt";"bytes";"image";"image/color";"image/png")
type Picture struct{}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{return color.RGBA{1,2,3,255}}
func main(){out:=bytes.NewBufferString("");err:=png.Encode(out,&Picture{});fmt.Println(out.Len()>0,err)}`
	if err = r.Run(context.Background(), callbackTourLoad(t, dir, source).File); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(output.Bytes(), []byte("true <nil>\n")) {
		t.Fatalf("Reset: %q", output.String())
	}
}

func TestGoSourceImageRetainedConsumerBoundary(t *testing.T) {
	dir := callbackTourModule(t)
	if err := os.Mkdir(filepath.Join(dir, "dep"), 0700); err != nil {
		t.Fatal(err)
	}
	dependency := `package dep
import("fmt";"image")
var saved image.Image
func Retain(m image.Image){saved=m;fmt.Println("dependency-retained")}`
	if err := os.WriteFile(filepath.Join(dir, "dep", "retain.go"), []byte(dependency), 0600); err != nil {
		t.Fatal(err)
	}
	source := `package main
import("image";"image/color";"example.com/callback-test/dep")
type Picture struct{}
` + imageReviewMethods + `
func(m *Picture)At(x,y int)color.Color{println("original-At");return color.RGBA{1,2,3,255}}
func main(){dep.Retain(&Picture{});println("after")}`
	path := filepath.Join(dir, "original.go")
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "run", "-mod=readonly", "-p", "2", path)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil || string(out) != "dependency-retained\nafter\n" {
		t.Fatalf("native retain control: %v %q", err, out)
	}
	var out bytes.Buffer
	r, err := interp.New(interp.Lang(syntax.LangBashPP), interp.Dir(dir), interp.GoSourceModuleDir(dir), interp.StdIO(nil, &out, &out))
	if err != nil {
		t.Fatal(err)
	}
	err = r.Run(context.Background(), callbackTourLoad(t, dir, source).File)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("retained image accepted: %v %q", err, out.String())
	}
	for _, marker := range []string{"dependency-retained", "original-At", "after"} {
		if strings.Contains(out.String(), marker) {
			t.Fatalf("retainer path executed: %q", out.String())
		}
	}
}
