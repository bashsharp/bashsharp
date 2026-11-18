package fixture

// Plain has no imports, so its local constant must retain the existing folded
// form and no import may be hoisted into this file.
func Plain() int {
	const width = len("abc")
	return width + WordBytes() + Limit()
}
