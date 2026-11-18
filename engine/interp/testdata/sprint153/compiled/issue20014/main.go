package main

import (
	"issue20014/a"
	"strings"
)

var fieldTrackInfo string

func main() {
	_ = a.T{}.GetX()
	if !strings.Contains(fieldTrackInfo, "issue20014/a.T.X") {
		panic("missing linker field-track data")
	}
}
