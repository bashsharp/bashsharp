// Positive control: the empty-interface form of the same shape was already
// supported before the interface-element correction.
package main

import "fmt"

type parcel struct {
	Tag string
	how interface{}
}

func main() {
	p := parcel{Tag: "boxed"}
	p.how = 2
	fmt.Println("parcel", p.Tag)
}
