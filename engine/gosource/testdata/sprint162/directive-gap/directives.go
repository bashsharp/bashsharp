package directives

//go:noinline
func Adjacent(x int) int { return x }

//go:noescape

func Separated(*byte)

func Before() {}

//go:nosplit

func After() {}
