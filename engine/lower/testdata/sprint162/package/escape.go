package escape

type Impl struct{}

func (*Impl) M() {}

func F(x *int) *int {
	return x
}
