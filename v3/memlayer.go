package iavl

import (
	"bytes"
	"fmt"
	"sync"
	"time"
)

// MemLayer represents an immutable in-memory tree layer with structural sharing
type MemLayer struct {
	id        int64
	root      *Node // Immutable root node
	version   int64 // Version this layer represents

	// Reference to the baseline tree from DiskLayer
	baseline *Node // The tree state from DiskLayer when this MemLayer was created

	createdAt time.Time

	// Tracks explicit deletions in this layer
	deletions map[string]bool // Key -> deleted flag

	// Whether this layer has changes from baseline
	hasChanges bool
}

// MemLayerManager manages multiple mem layers and handles compaction
type MemLayerManager struct {
	layers    []*MemLayer
	diskLayer *DiskLayer
	
	// Configuration
	maxLayers int
	
	// State
	mu       sync.RWMutex
	nextID   int64
}

// NewMemLayerManager creates a new layer manager
func NewMemLayerManager(diskLayer *DiskLayer, maxLayers int) *MemLayerManager {
	return &MemLayerManager{
		layers:    make([]*MemLayer, 0, maxLayers+1),
		diskLayer: diskLayer,
		maxLayers: maxLayers,
		nextID:    1,
	}
}

// Get retrieves a value from the layer stack using O(log n) search
func (m *MemLayerManager) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Search from latest to oldest layer
	for i := len(m.layers) - 1; i >= 0; i-- {
		layer := m.layers[i]
		
		// Check if explicitly deleted in this layer
		if layer.deletions[string(key)] {
			return nil, nil
		}
		
		// Try to get from this layer's tree
		if value := layer.root.get(key); value != nil {
			return value, nil
		}
	}
	
	// Not found in mem layers, query disk layer
	return m.diskLayer.Get(key)
}

// Set creates a new layer with the given changeset using DiskLayer as baseline
func (m *MemLayerManager) Set(changes []*KVPair) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Get current baseline from DiskLayer
	baselineRoot, err := m.diskLayer.GetTreeRoot()
	if err != nil {
		return err
	}

	// Create new layer with updated tree
	// We use the disk layer's root as baseline, then apply changes
	var newRoot *Node
	if baselineRoot != nil {
		// Use copy-on-write: read from disk, apply changes
		newRoot = m.updateFromDisk(baselineRoot, changes).markImmutable()
	} else {
		// Empty tree, build from changes
		for _, kv := range changes {
			if kv.Value != nil {
				newRoot = newRoot.insert(kv.Key, kv.Value).markImmutable()
			}
		}
	}

	// Track deletions
	deletions := make(map[string]bool)
	for _, kv := range changes {
		if kv.Value == nil {
			deletions[string(kv.Key)] = true
		}
	}

	newLayer := &MemLayer{
		id:          m.nextID,
		version:     m.nextID,
		root:        newRoot,
		baseline:    baselineRoot, // Reference to disk layer baseline
		createdAt:   time.Now(),
		deletions:   deletions,
		hasChanges:  true,
	}

	m.layers = append(m.layers, newLayer)
	m.nextID++

	// Check if compaction is needed
	if len(m.layers) > m.maxLayers {
		return m.compact()
	}

	return nil
}

// updateFromDisk updates nodes, reading from disk for shared subtrees
func (m *MemLayerManager) updateFromDisk(root *Node, changes []*KVPair) *Node {
	// Make a mutable copy to work with
	node := root
	if node.isImmutable {
		node = node.clone()
	}

	// Check if this node is being updated
	for _, kv := range changes {
		if bytes.Equal(kv.Key, node.key) {
			// This node is being updated
			if kv.Value == nil {
				// Deletion - merge children from disk
				node.left = m.updateFromDisk(node.left, changes)
				node.right = m.updateFromDisk(node.right, changes)
			} else {
				// Update value
				node.value = kv.Value
			}
			node.updateMetrics()
			return node.markImmutable()
		}
	}

	// Check children recursively
	node.left = m.updateFromDisk(node.left, changes)
	node.right = m.updateFromDisk(node.right, changes)

	// Update metrics
	node.updateMetrics()
	return node.markImmutable()
}

// compact merges all mem layers into the disk layer
func (m *MemLayerManager) compact() error {
	if len(m.layers) == 0 {
		return nil
	}
	
	// Get the latest layer's tree state
	latestLayer := m.layers[len(m.layers)-1]
	
	// Collect all changes from all layers (newest wins)
	changes := latestLayer.ExportChanges()
	
	// Apply to disk layer
	if err := m.diskLayer.ApplyChanges(changes); err != nil {
		return err
	}
	
	// Keep only the latest layer after compaction
	// This allows querying recent history
	m.layers = []*MemLayer{latestLayer}
	
	return nil
}

// GetVersion returns a snapshot of the tree at a specific layer version
func (m *MemLayerManager) GetVersion(version int64) (*ImmutableTree, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Find the layer
	for _, layer := range m.layers {
		if layer.version == version {
			// Return immutable tree
			return &ImmutableTree{
				root:    layer.root,
				version: layer.version,
				size:    layer.root.size,
				height:  layer.root.subtreeHeight,
			}, nil
		}
	}
	
	return nil, fmt.Errorf("version %d not found", version)
}

// Iterator creates an iterator that can cross layers
func (m *MemLayerManager) Iterator(start, end []byte) (*LayeredIterator, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return NewLayeredIterator(m.layers, m.diskLayer, start, end)
}

// LatestVersion returns the latest mem layer version
func (m *MemLayerManager) LatestVersion() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if len(m.layers) == 0 {
		return 0
	}
	return m.layers[len(m.layers)-1].version
}

// Get retrieves a value from a specific mem layer
func (l *MemLayer) Get(key []byte) []byte {
	if l.root == nil {
		return nil
	}
	return l.root.get(key)
}

// ExportChanges exports all changes from this layer
func (l *MemLayer) ExportChanges() []*KVPair {
	if l.root == nil {
		return nil
	}

	changes := make([]*KVPair, 0)

	// Recursively traverse the tree and collect all key-value pairs
	var traverse func(node *Node)
	traverse = func(node *Node) {
		if node == nil {
			return
		}

		if node.isLeaf() {
			// Check if this key was deleted in this layer
			if !l.deletions[string(node.key)] {
				changes = append(changes, &KVPair{
					Key:   node.key,
					Value: node.value,
				})
			}
		} else {
			// Traverse children
			traverse(node.left)
			traverse(node.right)
		}
	}

	traverse(l.root)
	return changes
}

// TreeIterator implements in-order traversal of a tree
type TreeIterator struct {
	start     []byte
	end       []byte
	stack     []*Node
	current   *Node
	err       error
}

// NewTreeIterator creates a new tree iterator starting from a root node
func NewTreeIterator(root *Node, start, end []byte) *TreeIterator {
	it := &TreeIterator{
		start: start,
		end:   end,
		stack: make([]*Node, 0),
	}

	// Initialize stack with leftmost path
	if root != nil {
		it.pushLeft(root)
		it.current = it.pop()
		// Skip nodes not in range
		for it.current != nil && !it.isInRange(it.current.key) {
			if it.current.right != nil {
				it.pushLeft(it.current.right)
			}
			it.current = it.pop()
		}
	}

	return it
}

// pushLeft pushes all left children from node onto the stack
func (it *TreeIterator) pushLeft(node *Node) {
	for node != nil {
		it.stack = append(it.stack, node)
		node = node.left
	}
}

// isInRange checks if a key is within the iterator's range
func (it *TreeIterator) isInRange(key []byte) bool {
	if it.start != nil && bytes.Compare(key, it.start) < 0 {
		return false
	}
	if it.end != nil && bytes.Compare(key, it.end) >= 0 {
		return false
	}
	return true
}

// pop pops the next node from the stack
func (it *TreeIterator) pop() *Node {
	if len(it.stack) == 0 {
		return nil
	}
	node := it.stack[len(it.stack)-1]
	it.stack = it.stack[:len(it.stack)-1]
	return node
}

// Next moves to the next key in the tree
func (it *TreeIterator) Next() {
	if it.current == nil {
		return
	}

	// Move to right subtree
	if it.current.right != nil {
		it.pushLeft(it.current.right)
	}

	// Get next node
	it.current = it.pop()

	// Skip nodes not in range
	for it.current != nil && !it.isInRange(it.current.key) {
		if it.current.right != nil {
			it.pushLeft(it.current.right)
		}
		it.current = it.pop()
	}
}

// Valid returns whether the iterator is valid
func (it *TreeIterator) Valid() bool {
	return it.err == nil && it.current != nil
}

// Key returns the current key
func (it *TreeIterator) Key() []byte {
	if !it.Valid() {
		return nil
	}
	return it.current.key
}

// Value returns the current value
func (it *TreeIterator) Value() []byte {
	if !it.Valid() {
		return nil
	}
	return it.current.value
}

// Error returns the last error
func (it *TreeIterator) Error() error {
	return it.err
}

// LayeredIterator iterates across multiple mem layers and disk layer
type LayeredIterator struct {
	layers    []*MemLayer
	diskLayer *DiskLayer
	start     []byte
	end       []byte
	current   *Node
	err       error

	// Internal state
	treeIter   *TreeIterator
	layerIdx   int
	seenKeys   map[string]bool // Track keys we've already returned
}

// NewLayeredIterator creates a new layered iterator
func NewLayeredIterator(layers []*MemLayer, diskLayer *DiskLayer, start, end []byte) (*LayeredIterator, error) {
	if len(layers) == 0 {
		return nil, fmt.Errorf("no layers available")
	}

	it := &LayeredIterator{
		layers:    layers,
		diskLayer: diskLayer,
		start:     start,
		end:       end,
		layerIdx:  len(layers) - 1, // Start from latest layer
		seenKeys:  make(map[string]bool),
	}

	// Initialize tree iterator for the latest layer
	it.treeIter = NewTreeIterator(layers[it.layerIdx].root, start, end)

	return it, nil
}

// Next moves to the next key across all layers
func (it *LayeredIterator) Next() {
	if it.err != nil {
		return
	}

	// Find next valid key across layers
	for {
		if it.treeIter == nil || !it.treeIter.Valid() {
			// Move to previous layer
			it.layerIdx--
			if it.layerIdx < 0 {
				// All mem layers exhausted
				it.treeIter = nil
				it.current = nil
				return
			}
			// Start iteration on this layer
			it.treeIter = NewTreeIterator(it.layers[it.layerIdx].root, it.start, it.end)
			continue
		}

		// Check if current key is valid (not deleted in a newer layer)
		key := it.treeIter.Key()
		keyStr := string(key)

		// Skip internal nodes (separator keys) - only return leaf nodes
		if it.treeIter.current != nil && !it.treeIter.current.isLeaf() {
			it.treeIter.Next()
			continue
		}

		// Check if this key was deleted in any newer layer
		if it.isDeletedInNewerLayer(key) {
			// Skip this key and continue
			it.treeIter.Next()
			continue
		}

		// Check if we've already seen this key (from a newer layer)
		if it.seenKeys[keyStr] {
			// Skip this key and continue
			it.treeIter.Next()
			continue
		}

		// Key is valid and new, return it
		it.seenKeys[keyStr] = true
		it.current = it.treeIter.current
		it.treeIter.Next()

		// After moving to next, check if we need to skip internal nodes
		for it.treeIter.Valid() && it.treeIter.current != nil && !it.treeIter.current.isLeaf() {
			it.treeIter.Next()
		}

		return
	}
}

// isDeletedInNewerLayer checks if a key was deleted in any layer newer than current
func (it *LayeredIterator) isDeletedInNewerLayer(key []byte) bool {
	keyStr := string(key)

	// Check all layers newer than current
	for i := it.layerIdx + 1; i < len(it.layers); i++ {
		if it.layers[i].deletions[keyStr] {
			return true
		}
	}

	return false
}

// Valid returns whether the iterator is valid
func (it *LayeredIterator) Valid() bool {
	if it.err != nil {
		return false
	}
	if it.treeIter == nil {
		return false
	}
	// Check if current key is valid
	if !it.treeIter.Valid() {
		return false
	}

	// Skip internal nodes (separator keys) - only return leaf nodes
	if it.treeIter.current != nil && !it.treeIter.current.isLeaf() {
		return false
	}

	key := it.treeIter.Key()
	return !it.isDeletedInNewerLayer(key)
}

// Key returns the current key
func (it *LayeredIterator) Key() []byte {
	if it.treeIter == nil {
		return nil
	}
	return it.treeIter.Key()
}

// Value returns the current value
func (it *LayeredIterator) Value() []byte {
	if it.treeIter == nil {
		return nil
	}
	return it.treeIter.Value()
}

// Error returns the last error
func (it *LayeredIterator) Error() error {
	return it.err
}

// Close closes the iterator
func (it *LayeredIterator) Close() error {
	return nil
}
