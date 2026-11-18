package main

import (
	"reflect"

	"example.com/rec/b"
)

func main() {
	b.F1(b.T{})
	b.F2(b.T{})
	// b.V is the variable, not a.V the type it is named like; b.U embeds
	// the type. Both keep package b's identity.
	if got := reflect.ValueOf(b.V).Type().Field(0).PkgPath; got != "example.com/rec/b" {
		panic(`PkgPath=` + got + ` for first field of b.V`)
	}
	if got := reflect.ValueOf(b.U).Type().Field(1).PkgPath; got != "example.com/rec/b" {
		panic(`PkgPath=` + got + ` for second field of b.U`)
	}
}
