package b

func G() interface{} { return struct{ _ []int }{} }

var X = G()
