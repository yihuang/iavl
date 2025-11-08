package iavl

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	corestore "cosmossdk.io/core/store"
)

// MutableTree represents a mutable IAVL tree with single-version persistence
type MutableTree struct {
	db     *Database
	logger Logger
	
	root     *Node
	version  int64
	size     int64
	height   int8
	
	// Cache for nodes
	nodeCache map[string]*Node
	
	// Working changes
	updates map[string]*Node // keypath -> node
	
	// Batch for efficient writes
	batch corestore.Batch
}

// NewMutableTree creates a new mutable tree
func NewMutableTree(db corestore.KVStoreWithBatch, cacheSize int, logger Logger, options ...Option) *MutableTree {
	opts := DefaultOptions()
	for _, opt := range options {
		opt(&opts)
	}
	
	tree := &MutableTree{
		db:        NewDatabase(db, logger),
		logger:    logger,
		root:      nil,
		version:   opts.InitialVersion - 1,
		nodeCache: make(map[string]*Node, cacheSize),
		updates:   make(map[string]*Node),
		batch:     db.NewBatch(),
	}
	
	// Load latest version if exists
	if latest, err := tree.GetLatestVersion(); err == nil && latest > 0 {
		tree.version = latest
	}
	
	return tree
}

// Get returns the value for a key
func (t *MutableTree) Get(key []byte) ([]byte, error) {
	// First try latest state (O(1) access)
	if value, err := t.db.LoadLatestState(key); err == nil && value != nil {
		return value, nil
	}
	
	// If not in latest state, tree might be empty or key doesn't exist
	if t.root == nil {
		return nil, nil
	}
	
	// Fall back to tree search (shouldn't normally happen with latest state)
	return t.get(t.root, key)
}

// Set sets a key-value pair in the tree
func (t *MutableTree) Set(key, value []byte) error {
	if len(key) == 0 {
		return errors.New("key cannot be empty")
	}
	
	if t.root == nil {
		// Empty tree, create new leaf node
		t.root = NewNode(key, value)
		t.size = 1
		t.height = 0
	} else {
		// Check if key already exists
		_, exists, err := t.getWithExists(t.root, key)
		if err != nil {
			return err
		}
		
		// Insert into tree
		t.root = t.insert(t.root, key, value, true)
		t.root.computeHash()
		
		// Update size if this is a new key
		if !exists {
			t.size++
		}
	}
	
	// Update latest state for O(1) access
	if err := t.db.SaveLatestState(key, value); err != nil {
		return err
	}
	
	return nil
}

// Remove removes a key from the tree
func (t *MutableTree) Remove(key []byte) ([]byte, error) {
	var oldValue []byte
	
	if t.root != nil {
		var newRoot *Node
		var err error
		var removed bool
		newRoot, oldValue, removed, err = t.remove(t.root, key, true)
		if err != nil {
			return nil, err
		}
		if removed {
			t.size--
		}
		if newRoot == nil {
			// Tree is now empty
			t.root = nil
			t.size = 0
			t.height = 0
		} else {
			t.root = newRoot
			t.root.computeHash()
		}
	}
	
	// Update latest state
	if oldValue != nil {
		if err := t.db.DeleteLatestState(key); err != nil {
			return nil, err
		}
	}
	
	return oldValue, nil
}

// SaveVersion saves the current tree as a version
func (t *MutableTree) SaveVersion() (int64, error) {
	if t.root == nil {
		return t.version, nil
	}
	
	// Compute root hash
	rootHash := t.root.computeHash()
	
	// Update version
	t.version++
	
	// Save all nodes
	if err := t.saveNodes(t.root); err != nil {
		return 0, err
	}
	
	// Save version metadata
	meta := &VersionMetadata{
		Version:      t.version,
		MerkleRoot:   rootHash,
		Size:         t.size,
		Height:       t.height,
		Timestamp:    time.Now().UnixNano(),
	}
	if err := t.db.SaveVersionMetadata(meta); err != nil {
		return 0, err
	}
	
	// Commit batch
	if err := t.db.Commit(); err != nil {
		return 0, err
	}
	
	return t.version, nil
}

// LoadVersion loads a specific version
func (t *MutableTree) LoadVersion(version int64) error {
	// For v3, loading a historical version requires rebuilding the tree
	// This is a limitation of the single-version storage approach
	
	if version == t.version {
		// Already at this version
		return nil
	}
	
	// Load version metadata
	meta, err := t.db.LoadVersionMetadata(version)
	if err != nil {
		return err
	}
	if meta == nil {
		return fmt.Errorf("version %d not found", version)
	}
	
	// For now, we don't support loading historical versions
	// This would require storing all nodes for that version or rebuilding from latest
	return fmt.Errorf("loading historical versions not yet implemented in v3")
}

// GetLatestVersion returns the latest saved version
func (t *MutableTree) GetLatestVersion() (int64, error) {
	// Iterate through metadata to find latest
	itr, err := t.db.MetadataIterator()
	if err != nil {
		return 0, err
	}
	defer itr.Close()
	
	var latest int64 = 0
	for ; itr.Valid(); itr.Next() {
		// Extract version from key
		if len(itr.Key()) == 9 && itr.Key()[0] == metadataPrefix {
			v := int64(binary.BigEndian.Uint64(itr.Key()[1:]))
			if v > latest {
				latest = v
			}
		}
	}
	
	return latest, nil
}

// GetImmutable returns an immutable snapshot of the current tree
func (t *MutableTree) GetImmutable() *ImmutableTree {
	return &ImmutableTree{
		root:    t.root,
		version: t.version,
		size:    t.size,
		height:  t.height,
	}
}

// Size returns the number of keys in the tree
func (t *MutableTree) Size() int64 {
	return t.size
}

// Height returns the height of the tree
func (t *MutableTree) Height() int8 {
	if t.root == nil {
		return 0
	}
	return t.root.subtreeHeight
}

// Hash returns the root hash of the tree
func (t *MutableTree) Hash() []byte {
	if t.root == nil {
		return sha256.New().Sum(nil)
	}
	return t.root.computeHash()
}

// Iterator returns an iterator over the tree
func (t *MutableTree) Iterator(start, end []byte) (*Iterator, error) {
	// For v3, iterate over latest state
	return NewIterator(t.db, start, end)
}

// Internal methods

func (t *MutableTree) get(node *Node, key []byte) ([]byte, error) {
	value, _, _ := t.getWithExists(node, key)
	return value, nil
}

func (t *MutableTree) getWithExists(node *Node, key []byte) ([]byte, bool, error) {
	if node.isLeaf() {
		if bytes.Equal(node.key, key) {
			return node.value, true, nil
		}
		return nil, false, nil
	}
	
	if bytes.Compare(key, node.key) < 0 {
		return t.getWithExists(node.left, key)
	}
	return t.getWithExists(node.right, key)
}

func (t *MutableTree) insert(node *Node, key, value []byte, copyOnWrite bool) *Node {
	if node == nil {
		return NewNode(key, value)
	}
	
	if copyOnWrite {
		// Make a copy for structural sharing
		node = node.clone()
	}
	
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
		
		// New internal node with key as separator
		newNode := NewInnerNode(key, left, right)
		return newNode
	}
	
	// Insert into appropriate child
	if bytes.Compare(key, node.key) < 0 {
		node.left = t.insert(node.left, key, value, copyOnWrite)
	} else {
		node.right = t.insert(node.right, key, value, copyOnWrite)
	}
	
	// Rebalance if necessary
	return t.rebalance(node)
}

func (t *MutableTree) remove(node *Node, key []byte, copyOnWrite bool) (*Node, []byte, bool, error) {
	if node == nil {
		return nil, nil, false, nil
	}
	
	var oldValue []byte
	
	if node.isLeaf() {
		if bytes.Equal(node.key, key) {
			oldValue = node.value
			// Remove this node
			return nil, oldValue, true, nil
		}
		return node, oldValue, false, nil
	}
	
	var err error
	var removed bool
	if bytes.Compare(key, node.key) < 0 {
		var newLeft *Node
		newLeft, oldValue, removed, err = t.remove(node.left, key, copyOnWrite)
		if err != nil {
			return nil, oldValue, removed, err
		}
		if copyOnWrite {
			node = node.clone()
		}
		node.left = newLeft
	} else {
		var newRight *Node
		newRight, oldValue, removed, err = t.remove(node.right, key, copyOnWrite)
		if err != nil {
			return nil, oldValue, removed, err
		}
		if copyOnWrite {
			node = node.clone()
		}
		node.right = newRight
	}
	
	if removed {
		// Key was found and removed
		if node.left == nil && node.right == nil {
			// Node has no children, remove it
			return nil, oldValue, true, nil
		} else if node.left == nil {
			// Only right child
			return node.right, oldValue, true, nil
		} else if node.right == nil {
			// Only left child
			return node.left, oldValue, true, nil
		}
	}
	
	// Rebalance if necessary
	return t.rebalance(node), oldValue, removed, nil
}

func (t *MutableTree) rebalance(node *Node) *Node {
	if node == nil {
		return nil
	}
	
	node.subtreeHeight = maxInt8(getHeight(node.left), getHeight(node.right)) + 1
	node.size = getSize(node.left) + getSize(node.right)
	
	leftHeight := getHeight(node.left)
	rightHeight := getHeight(node.right)
	
	// Left heavy
	if leftHeight > rightHeight+1 {
		if getHeight(node.left.left) >= getHeight(node.left.right) {
			// Left-left case
			return t.rotateRight(node)
		}
		// Left-right case
		node.left = t.rotateLeft(node.left)
		return t.rotateRight(node)
	}
	
	// Right heavy
	if rightHeight > leftHeight+1 {
		if getHeight(node.right.right) >= getHeight(node.right.left) {
			// Right-right case
			return t.rotateLeft(node)
		}
		// Right-left case
		node.right = t.rotateRight(node.right)
		return t.rotateLeft(node)
	}
	
	return node
}

func (t *MutableTree) rotateLeft(node *Node) *Node {
	right := node.right
	node.right = right.left
	right.left = node
	
	node.subtreeHeight = maxInt8(getHeight(node.left), getHeight(node.right)) + 1
	node.size = getSize(node.left) + getSize(node.right)
	right.subtreeHeight = maxInt8(getHeight(right.left), getHeight(right.right)) + 1
	right.size = getSize(right.left) + getSize(right.right)
	
	return right
}

func (t *MutableTree) rotateRight(node *Node) *Node {
	left := node.left
	node.left = left.right
	left.right = node
	
	node.subtreeHeight = maxInt8(getHeight(node.left), getHeight(node.right)) + 1
	node.size = getSize(node.left) + getSize(node.right)
	left.subtreeHeight = maxInt8(getHeight(left.left), getHeight(left.right)) + 1
	left.size = getSize(left.left) + getSize(left.right)
	
	return left
}

func (t *MutableTree) saveNodes(node *Node) error {
	if node == nil {
		return nil
	}
	
	// Save this node
	if err := t.db.SaveNode(node.key, node); err != nil {
		return err
	}
	
	// Save children
	if err := t.saveNodes(node.left); err != nil {
		return err
	}
	if err := t.saveNodes(node.right); err != nil {
		return err
	}
	
	return nil
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

// ImmutableTree represents an immutable snapshot
type ImmutableTree struct {
	root    *Node
	version int64
	size    int64
	height  int8
}

// Size returns the number of keys
func (t *ImmutableTree) Size() int64 {
	return t.size
}

// Version returns the version
func (t *ImmutableTree) Version() int64 {
	return t.version
}

// Height returns the height
func (t *ImmutableTree) Height() int8 {
	return t.height
}

// Hash returns the root hash
func (t *ImmutableTree) Hash() []byte {
	if t.root == nil {
		return sha256.New().Sum(nil)
	}
	return t.root.computeHash()
}

// clone creates a deep copy of the mutable tree
func (t *MutableTree) clone() *MutableTree {
	newTree := &MutableTree{
		db:        t.db,
		logger:    t.logger,
		root:      nil,
		version:   t.version,
		size:      t.size,
		height:    t.height,
		nodeCache: make(map[string]*Node, len(t.nodeCache)),
		updates:   make(map[string]*Node, len(t.updates)),
	}
	
	// Copy root
	if t.root != nil {
		newTree.root = t.root.clone()
	}
	
	// Copy caches
	for k, v := range t.nodeCache {
		newTree.nodeCache[k] = v
	}
	for k, v := range t.updates {
		newTree.updates[k] = v
	}
	
	return newTree
}
