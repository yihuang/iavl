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

	// Immutable flag for copy-on-write
	isImmutable bool
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
			isImmutable:    n.isImmutable, // Preserve immutability flag
		}
	}
	return &Node{
		key:            CloneBytes(n.key),
		left:           n.left,
		right:          n.right,
		subtreeHeight:  n.subtreeHeight,
		size:           n.size,
		isImmutable:    n.isImmutable, // Preserve immutability flag
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

// immutable marks the node as immutable (for copy-on-write)
func (n *Node) markImmutable() *Node {
	if n == nil {
		return nil
	}
	newNode := n.clone()
	newNode.isImmutable = true
	return newNode
}

// cloneOnWrite creates a mutable copy if this node is immutable
func (n *Node) cloneOnWrite() *Node {
	if !n.isImmutable {
		return n
	}
	newNode := n.clone()
	newNode.isImmutable = false
	return newNode
}

// get returns the value for a key (searches this subtree)
func (n *Node) get(key []byte) []byte {
	if n == nil {
		return nil
	}
	
	if n.isLeaf() {
		if bytes.Equal(n.key, key) {
			return n.value
		}
		return nil
	}
	
	if bytes.Compare(key, n.key) < 0 {
		return n.left.get(key)
	}
	return n.right.get(key)
}

// update applies a changeset to the subtree and returns new root
func (n *Node) update(changes []*KVPair) *Node {
	if n == nil {
		// Start from scratch
		var newRoot *Node
		for _, kv := range changes {
			if kv.Value != nil {
				newRoot = newRoot.insert(kv.Key, kv.Value)
			}
		}
		return newRoot
	}
	
	// Create mutable copy
	node := n.cloneOnWrite()
	
	// Apply each change
	for _, kv := range changes {
		if kv.Value == nil {
			// Deletion
			node = node.remove(kv.Key)
		} else {
			// Insert/Update
			node = node.insert(kv.Key, kv.Value)
		}
	}
	
	return node
}

// insert inserts a key-value pair into the subtree (returns new node)
func (n *Node) insert(key, value []byte) *Node {
	if n == nil {
		return NewNode(key, value)
	}
	
	// Always work with mutable copy
	node := n.cloneOnWrite()
	
	if node.isLeaf() {
		// Compare with existing leaf
		cmp := bytes.Compare(key, node.key)
		if cmp == 0 {
			// Update existing key
			node.value = value
			return node
		}
		
		// Convert leaf to internal node
		var left, right *Node
		if cmp < 0 {
			left = NewNode(key, value)
			right = node
		} else {
			left = node
			right = NewNode(key, value)
		}
		
		// New internal node
		newNode := NewInnerNode(key, left, right)
		newNode.isImmutable = true
		return newNode
	}
	
	// Insert into appropriate child
	if bytes.Compare(key, node.key) < 0 {
		node.left = node.left.insert(key, value)
	} else {
		node.right = node.right.insert(key, value)
	}
	
	// Rebalance if necessary
	return node.rebalance()
}

// remove removes a key from the subtree (returns new node)
func (n *Node) remove(key []byte) *Node {
	if n == nil {
		return nil
	}
	
	// Always work with mutable copy
	node := n.cloneOnWrite()
	
	if node.isLeaf() {
		if bytes.Equal(node.key, key) {
			// Remove this node
			return nil
		}
		return node
	}
	
	if bytes.Compare(key, node.key) < 0 {
		node.left = node.left.remove(key)
	} else {
		node.right = node.right.remove(key)
	}
	
	// After removal, need to rebalance or adjust
	if node.left == nil {
		return node.right
	}
	if node.right == nil {
		return node.left
	}
	
	// Both children exist, need to find successor or rebalance
	return node.rebalance()
}

// rebalance rebalances the tree
func (n *Node) rebalance() *Node {
	if n == nil {
		return nil
	}
	
	n.subtreeHeight = maxInt8(getHeight(n.left), getHeight(n.right)) + 1
	n.size = getSize(n.left) + getSize(n.right)
	
	leftHeight := getHeight(n.left)
	rightHeight := getHeight(n.right)
	
	// Left heavy
	if leftHeight > rightHeight+1 {
		if getHeight(n.left.left) >= getHeight(n.left.right) {
			// Left-left case
			return n.rotateRight()
		}
		// Left-right case
		n.left = n.left.rotateLeft()
		return n.rotateRight()
	}
	
	// Right heavy
	if rightHeight > leftHeight+1 {
		if getHeight(n.right.right) >= getHeight(n.right.left) {
			// Right-right case
			return n.rotateLeft()
		}
		// Right-left case
		n.right = n.right.rotateRight()
		return n.rotateLeft()
	}
	
	return n
}

func (n *Node) rotateLeft() *Node {
	right := n.right
	n.right = right.left
	right.left = n
	
	n.subtreeHeight = maxInt8(getHeight(n.left), getHeight(n.right)) + 1
	n.size = getSize(n.left) + getSize(n.right)
	right.subtreeHeight = maxInt8(getHeight(right.left), getHeight(right.right)) + 1
	right.size = getSize(right.left) + getSize(right.right)
	
	right.isImmutable = true
	return right
}

func (n *Node) rotateRight() *Node {
	left := n.left
	n.left = left.right
	left.right = n
	
	n.subtreeHeight = maxInt8(getHeight(n.left), getHeight(n.right)) + 1
	n.size = getSize(n.left) + getSize(n.right)
	left.subtreeHeight = maxInt8(getHeight(left.left), getHeight(left.right)) + 1
	left.size = getSize(left.left) + getSize(left.right)
	
	left.isImmutable = true
	return left
}

func getHeight(node *Node) int8 {
	if node == nil {
		return -1
	}
	return node.subtreeHeight
}

func getSize(node *Node) int64 {
	if node == nil {
		return 0
	}
	return node.size
}

// updateMetrics updates the height and size metrics of the node
func (n *Node) updateMetrics() {
	n.subtreeHeight = maxInt8(getHeight(n.left), getHeight(n.right)) + 1
	n.size = getSize(n.left) + getSize(n.right)
}
