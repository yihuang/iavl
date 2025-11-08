package iavl

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
)

// Node represents a node in the IAVL+ tree
type Node struct {
	key     []byte
	value   []byte
	hash    []byte
	
	// Child nodes (not persisted, only in-memory during tree construction)
	left  *Node
	right *Node
	
	// Metadata
	subtreeHeight int8
	size          int64 // Number of leaf nodes in this subtree
}

// NewNode creates a new leaf node
func NewNode(key, value []byte) *Node {
	return &Node{
		key:            CloneBytes(key),
		value:          CloneBytes(value),
		subtreeHeight:  0,
		size:           1,
	}
}

// NewInnerNode creates a new internal node
func NewInnerNode(key []byte, left, right *Node) *Node {
	return &Node{
		key:            CloneBytes(key),
		left:           left,
		right:          right,
		subtreeHeight:  maxInt8(left.subtreeHeight, right.subtreeHeight) + 1,
		size:           left.size + right.size,
	}
}

// isLeaf returns true if this is a leaf node
func (n *Node) isLeaf() bool {
	return n.subtreeHeight == 0
}

// String returns a string representation of the node
func (n *Node) String() string {
	if n.isLeaf() {
		return fmt.Sprintf("Node{leaf: %s=%s, size: %d, height: %d}", 
			string(n.key), string(n.value), n.size, n.subtreeHeight)
	}
	return fmt.Sprintf("Node{inner: key: %s, size: %d, height: %d}", 
		string(n.key), n.size, n.subtreeHeight)
}

// clone creates a shallow copy of the node (for tree mutation)
func (n *Node) clone() *Node {
	if n.isLeaf() {
		return &Node{
			key:            CloneBytes(n.key),
			value:          CloneBytes(n.value),
			subtreeHeight:  n.subtreeHeight,
			size:           n.size,
		}
	}
	return &Node{
		key:            CloneBytes(n.key),
		left:           n.left,
		right:          n.right,
		subtreeHeight:  n.subtreeHeight,
		size:           n.size,
	}
}

// computeHash computes the hash of the node
func (n *Node) computeHash() []byte {
	if n.hash != nil {
		return n.hash
	}
	
	h := sha256.New()
	if n.isLeaf() {
		// For leaf nodes, hash key + value
		h.Write(n.key)
		h.Write(n.value)
	} else {
		// For internal nodes, hash left + right
		if n.left == nil || n.right == nil {
			panic("internal node must have both children")
		}
		n.left.computeHash()
		n.right.computeHash()
		h.Write(n.left.hash)
		h.Write(n.right.hash)
	}
	n.hash = h.Sum(nil)
	return n.hash
}

// serialize serializes the node to bytes
func (n *Node) serialize() []byte {
	var buf bytes.Buffer
	
	// Write subtree height
	binary.Write(&buf, binary.BigEndian, n.subtreeHeight)
	
	// Write size
	binary.Write(&buf, binary.BigEndian, n.size)
	
	// Write key length and key
	binary.Write(&buf, binary.BigEndian, int64(len(n.key)))
	buf.Write(n.key)
	
	if n.isLeaf() {
		// Write value length and value
		binary.Write(&buf, binary.BigEndian, int64(len(n.value)))
		buf.Write(n.value)
	} else {
		// Write child hashes
		if n.left == nil || n.right == nil {
			panic("internal node must have both children")
		}
		n.left.computeHash()
		n.right.computeHash()
		buf.Write(n.left.hash)
		buf.Write(n.right.hash)
	}
	
	return buf.Bytes()
}

// deserializeNode deserializes bytes to a node
func deserializeNode(data []byte) (*Node, error) {
	if len(data) == 0 {
		return nil, nil
	}
	
	buf := bytes.NewBuffer(data)
	
	var height int8
	if err := binary.Read(buf, binary.BigEndian, &height); err != nil {
		return nil, err
	}
	
	var size int64
	if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
		return nil, err
	}
	
	// Read key
	var keyLen int64
	if err := binary.Read(buf, binary.BigEndian, &keyLen); err != nil {
		return nil, err
	}
	key := make([]byte, keyLen)
	if _, err := buf.Read(key); err != nil {
		return nil, err
	}
	
	n := &Node{
		key:            key,
		subtreeHeight:  height,
		size:           size,
	}
	
	if height == 0 {
		// Leaf node - read value
		var valLen int64
		if err := binary.Read(buf, binary.BigEndian, &valLen); err != nil {
			return nil, err
		}
		value := make([]byte, valLen)
		if _, err := buf.Read(value); err != nil {
			return nil, err
		}
		n.value = value
	} else {
		// Internal node - read child hashes
		leftHash := make([]byte, 32)
		if _, err := buf.Read(leftHash); err != nil {
			return nil, err
		}
		rightHash := make([]byte, 32)
		if _, err := buf.Read(rightHash); err != nil {
			return nil, err
		}
		// We don't deserialize child pointers, they'll be loaded on-demand
		n.left = &Node{hash: leftHash}
		n.right = &Node{hash: rightHash}
	}
	
	return n, nil
}

// maxInt8 returns the maximum of two int8 values
func maxInt8(a, b int8) int8 {
	if a > b {
		return a
	}
	return b
}
