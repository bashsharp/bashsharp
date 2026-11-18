// Copyright (c) 2026, the bashy authors.
// See LICENSE for licensing information.

//go:build full

package interp_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bashsharp/bashsharp/engine/gosource"
)

func TestGoSourceCompositeHandoff(t *testing.T) {
	for name, source := range map[string]string{
		"map_pointer_operands": `package main
var calls string
var p=new(int)
var values map[*int]int
func key()*int{calls+="k";return p}
func value()int{calls+="v";return 7}
func main(){values=map[*int]int{key():value()};if calls!="kv" || values[p]!=7 {panic("map handoff")}}`,
		"imported_address": `package main
import "go/ast"
var calls int
func label()string{calls++;return "item"}
func main(){xs:=[]ast.Expr{&ast.Ident{Name:label()}};p:=xs[0].(*ast.Ident);if calls!=1 || p.Name!="item" || xs[0].Pos()!=0 {panic("imported address")}}`,
		"native_nested_scalar_result": `package main
import "go/ast"
var calls int
func index()int{calls++;return 0}
func visit(xs []ast.Expr){println(xs[index()].Pos());if calls!=1 {panic("receiver repeated")}}
func main(){visit([]ast.Expr{&ast.Ident{}})}`,
		"map_operand_panic": `package main
var calls string
var values map[*int]int
func key()*int{calls+="k";panic("key")}
func value()int{calls+="v";return 7}
func main(){defer func(){if recover()!="key" || calls!="k" || values!=nil {panic("repeated operand")}}();values=map[*int]int{key():value()};panic("continued")}`,
		"generic_field_interface": `package main
type reader[T comparable] interface{read()T}
type box[T comparable] struct{value T}
func(p *box[T])read()T{return p.value}
type outer struct{item box[string]}
func main(){var o outer;o.item.value="ok";p:=&o.item;_ = reader[string](&o.item);i:=reader[string](&o.item);if i.read()!="ok" || i.(*box[string])!=p {panic("generic interface")}}`,
	} {
		t.Run(name, func(t *testing.T) { differGoSource(t, source, nil, "") })
	}
}

func TestGoSourceCompositeMappedPackage(t *testing.T) {
	differGoSourceModule(t, filepath.Join("testdata", "gosource-composite-handoff", "mapped"))
}
func TestGoSourceCompositeRejectsInvalid(t *testing.T) {
	for name, source := range map[string]string{
		"pointer_key":        `package main;func main(){_ = map[*int]int{"bad":1}}`,
		"map_address":        `package main;func main(){m:=map[int]int{1:2};_ = &m[1]}`,
		"imported_interface": `package main;import "go/ast";func main(){_ = []ast.Expr{&ast.File{}}}`,
		"generic_interface":  `package main;type I[T any] interface{Read()T};type B[T any] struct{};func main(){_ = I[int](&B[int]{})}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := gosource.Parse(strings.NewReader(source), "invalid.go", gosource.Options{RunMain: true}); err == nil {
				t.Fatal("invalid source accepted")
			}
		})
	}
}
