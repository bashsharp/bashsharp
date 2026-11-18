package a

type Int int
type IntAlias = Int
type IntAlias2 = IntAlias

type S struct {
	Int
	IntAlias
	IntAlias2
}
