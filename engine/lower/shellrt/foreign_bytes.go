package shellrt

// ForeignBytesProjection is the shell view of a typed foreign bytes result.
// Conversion preserves every byte, including invalid UTF-8; nil and empty
// slices both project empty without altering their typed storage.
func ForeignBytesProjection(value []byte) string { return string(value) }
