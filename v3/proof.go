package iavl

import (
	"bytes"
	"crypto/sha256"
)

// Proof represents a merkle proof for a key-value pair
type Proof struct {
	Key    []byte
	Value  []byte
	Root   []byte
	Hashes [][]byte // Sibling hashes from leaf to root
}

// Verify verifies the proof
func (p *Proof) Verify() bool {
	// Compute the hash path
	hash := sha256.New()
	hash.Write(p.Key)
	hash.Write(p.Value)
	leafHash := hash.Sum(nil)
	
	// Traverse up the tree
	currentHash := leafHash
	for _, siblingHash := range p.Hashes {
		// Combine with sibling (sorted)
		combined := make([]byte, 0, len(currentHash)+len(siblingHash))
		if bytes.Compare(currentHash, siblingHash) < 0 {
			combined = append(combined, currentHash...)
			combined = append(combined, siblingHash...)
		} else {
			combined = append(combined, siblingHash...)
			combined = append(combined, currentHash...)
		}
		
		h := sha256.New()
		h.Write(combined)
		currentHash = h.Sum(nil)
	}
	
	// Compare with root
	return bytes.Equal(currentHash, p.Root)
}

// BuildProof builds a proof for a key (not yet implemented)
func (t *MutableTree) BuildProof(key []byte) (*Proof, error) {
	// TODO: Implement proof generation
	return nil, nil
}
