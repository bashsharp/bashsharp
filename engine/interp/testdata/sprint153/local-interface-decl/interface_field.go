// A locally declared interface with method specifications, referenced by a
// struct field. The dependency helper must either materialise the interface
// faithfully or omit the referencing struct; it must never emit a declaration
// that does not compile.
package main

import "fmt"

type stamp interface {
	mark() string
}

type seal struct {
	Grade int
}

func (s seal) mark() string { return "sealed" }

type parcel struct {
	Tag string
	how stamp
}

func main() {
	p := parcel{Tag: "boxed"}
	p.how = seal{Grade: 2}
	fmt.Println("parcel", p.Tag, p.how.mark())
}
