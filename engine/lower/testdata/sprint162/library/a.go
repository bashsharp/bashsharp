package library

import "fmt"

type Thing struct {
	Name string
}

var Initialized bool

func init() {
	Initialized = true
}

func (t Thing) String() string {
	return fmt.Sprintf("thing:%s", t.Name)
}
