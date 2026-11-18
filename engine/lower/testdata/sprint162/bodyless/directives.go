package main

import _ "unsafe"

//go:noescape
func Load(p *uint64) uint64

//go:nosplit
//go:noescape
func Store(p *uint64, v uint64)

//go:linkname nanotime runtime.nanotime
func nanotime() int64

func main() {
	var x uint64
	Store(&x, x)
	println(Load(&x), nanotime())
}
