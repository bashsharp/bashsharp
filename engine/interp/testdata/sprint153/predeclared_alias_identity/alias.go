// byte is uint8 and rune is int32 in dynamic type identity: a type switch
// and an assertion match either spelling, and slices of either spelling
// assign to each other.
package main

import "fmt"

func kind(x interface{}) string {
	switch x.(type) {
	case uint8:
		return "uint8"
	case int32:
		return "int32"
	}
	return "other"
}

func main() {
	fmt.Println(kind(byte(1)), kind(uint8(2)), kind(rune(3)), kind(int32(4)), kind(5))
	var x interface{} = byte(7)
	b, ok := x.(byte)
	u, ok2 := x.(uint8)
	fmt.Println(b, ok, u, ok2)
	x = int32(9)
	r, ok := x.(rune)
	fmt.Println(r, ok)
	var bs []byte = []uint8{1, 2}
	var us []uint8 = []byte{3}
	fmt.Println(bs, us, len(bs)+len(us))
}
