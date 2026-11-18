//go:build full

package interp_test

import "testing"

func TestGoSourceUnwrapThreeModes(t *testing.T) {
	for name, source := range map[string]string{
		"nested_identity": `package main
import("fmt";"errors")
type wrapped struct{msg string;err error}
func(w wrapped)Error()string{return w.msg}
func(w wrapped)Unwrap()error{return w.err}
func main(){leaf:=errors.New("leaf");inner:=wrapped{"inner",leaf};outer:=wrapped{"outer",inner};fmt.Println(errors.Unwrap(nil)==nil,errors.Unwrap(wrapped{"nil",nil})==nil,errors.Unwrap(leaf)==nil);fmt.Println(errors.Unwrap(inner)==leaf,errors.Unwrap(outer)==inner,errors.Is(outer,leaf))}`,
		"pointer_state_reentry": `package main
import("fmt";"errors")
type wrapped struct{err error;calls int}
func(w *wrapped)Error()string{return "wrapped"}
func(w *wrapped)Unwrap()error{w.calls++;return errors.Unwrap(w.err)}
func main(){leaf:=errors.New("leaf");inside:=fmt.Errorf("inside: %w",leaf);w:=wrapped{inside,0};fmt.Println(errors.Is(&w,leaf),w.calls);fmt.Println(errors.Unwrap(&w)==leaf,w.calls)}`,
		"pointer_state_reentry_replaced_error": `package main
import("fmt";"errors")
type wrapped struct{err error;calls int}
func(w *wrapped)Error()string{return "wrapped"}
func(w *wrapped)Unwrap()error{w.calls++;return errors.Unwrap(w.err)}
func main(){first:=errors.New("first");second:=errors.New("second");inside:=fmt.Errorf("inside: %w",first);replacement:=fmt.Errorf("replacement: %w",second);w:=wrapped{inside,0};fmt.Println(errors.Is(&w,first),w.calls);w.err=replacement;fmt.Println(errors.Is(&w,second),w.calls);fmt.Println(errors.Is(&w,first),w.calls)}`,

		"typed_nil_result": `package main
import("fmt";"errors";"io/fs")
type wrapped struct{err error}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{return w.err}
func main(){var ptr *fs.PathError;var err error=ptr;result:=errors.Unwrap(wrapped{err});fmt.Println(result==nil,result==err,errors.Is(wrapped{err},err))}`,
		"local_typed_nil_result": `package main
import("fmt";"errors")
type E int
func(e *E)Error()string{return "nil E"}
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{return (*E)(nil)}
func main(){result:=errors.Unwrap(wrapped{});fmt.Println(result==nil)}`,
		"native_outer_wrapper": `package main
import("fmt";"errors")
type wrapped struct{err error}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{return w.err}
func main(){leaf:=errors.New("leaf");outer:=fmt.Errorf("outer: %w",wrapped{leaf});fmt.Println(errors.Is(outer,leaf),errors.Unwrap(errors.Unwrap(outer))==leaf)}`,
		"panic": `package main
import("fmt";"errors")
type wrapped struct{}
func(w wrapped)Error()string{return "wrapped"}
func(w wrapped)Unwrap()error{panic("unwrap panic")}
func main(){defer func(){fmt.Println(recover())}();errors.Unwrap(wrapped{});println("after")}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}
