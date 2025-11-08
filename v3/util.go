package iavl

// Utility functions

// CloneBytes clones a byte slice
func CloneBytes(b []byte) []byte {
	if b == nil {
		return nil
	}
	c := make([]byte, len(b))
	copy(c, b)
	return c
}
