package interp

import (
	"crypto/sha256"
	"io"
	"os"
)

// executableContentHash checks the full image, unlike the fast PE probe used
// to rule out non-matching candidates before hashing. A copied Bashy shell
// must match all bytes before it receives Bashy-specific environment handling.
func executableContentHash(path string) ([32]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
