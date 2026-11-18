package shellrt

// ReceiverProjection reflects the source method receiver binding, which exposes
// its pointee's value to shell expansion. Ordinary pointer variables still use
// KindPointer and project empty. Nil receivers stay empty without dereferencing.
func ReceiverProjection[T any](receiver *T) string {
	if receiver == nil {
		return ""
	}
	return Project(*receiver, KindInterface)
}
