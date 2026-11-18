package main

import (
	"encoding/json"
	"fmt"
)

type J struct {
	N int
}

func (j J) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf("{\"doubled\":%d}", j.N*2)), nil
}

func main() {
	out, err := json.Marshal(J{21})
	fmt.Println(string(out), err)
}
