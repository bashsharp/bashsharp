package a

type reader[T comparable] interface{ read() T }
type box[T comparable] struct{ value T }

func (p *box[T]) read() T { return p.value }

type outer struct{ item box[string] }

func Value() any {
	var o outer
	o.item.value = "mapped"
	p := &o.item
	i := reader[string](&o.item)
	if i.read() != "mapped" || i.(*box[string]) != p {
		panic("identity")
	}
	_ = reader[string](&o.item)
	return i
}
