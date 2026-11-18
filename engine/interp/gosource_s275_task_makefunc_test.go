//go:build full

package interp_test

import (
	"strings"
	"testing"
)

func TestS275TaskOwnedMakeFunc(t *testing.T) {
	src := `package main
import "reflect"
type T struct{}
func (T) M() { println("method") }
func impl([]reflect.Value) []reflect.Value { println("made"); return nil }
func main() {
	done := make(chan bool)
	finished := make(chan bool, 2)
	go func() {
		f := reflect.MakeFunc(reflect.TypeOf((func())(nil)), impl).Interface().(func())
		done <- true
		f()
		finished <- true
	}()
	<-done
	go func() {
		f := reflect.ValueOf(T{}).Method(0).Interface().(func())
		done <- true
		f()
		finished <- true
	}()
	<-done
	<-finished
	<-finished
}`
	_, stderr, err := runGoSource(t, "s275-task-owned-makefunc", src)
	if err != nil || strings.Count(stderr, "made\n") != 1 || strings.Count(stderr, "method\n") != 1 {
		t.Fatalf("task-owned made function: stderr=%q err=%v", stderr, err)
	}
}
