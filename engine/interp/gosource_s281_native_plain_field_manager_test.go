//go:build full

package interp_test

// Sprint: #281; Story: #809; Story-ID: fac7e14af4a8

import "testing"

func TestS281ManagerPlainNativeFieldTuple(t *testing.T) {
	const source = `package main
import ("debug/dwarf"; "fmt")
type holder struct { pairs [][2]uint64 }
func main() {
	entry := &dwarf.Entry{Field: []dwarf.Field{
		{Attr:dwarf.AttrLowpc, Val:uint64(10)},
		{Attr:dwarf.AttrHighpc, Class:dwarf.ClassAddress, Val:uint64(20)},
	}}
	var box holder
	var err error
	box.pairs, err = new(dwarf.Data).Ranges(entry)
	fmt.Println(err, len(box.pairs), box.pairs[0][0], box.pairs[0][1])
	box.pairs, err = new(dwarf.Data).Ranges(&dwarf.Entry{Field: []dwarf.Field{{Attr: dwarf.AttrName, Val: "no-ranges"}}})
	fmt.Println(err, box.pairs == nil, len(box.pairs))
	box.pairs = [][2]uint64{}
	fmt.Println(box.pairs == nil, len(box.pairs))
}`
	differGoSource(t, source, nil, "")
}
