package lib

// Exported for the external test only, as go/types' util_test.go does.
func CmpN(a, b *Sym) int { return Compare(a, b) }
