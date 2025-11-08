package iavl

import (
	"fmt"
	"sync"
	"time"

	corestore "cosmossdk.io/core/store"
)

// MemLayer represents an immutable in-memory tree layer
type MemLayer struct {
	id        int64
	tree      *MutableTree
	parent    *MemLayer
	createdAt time.Time
	
	// Layer-specific state
	layerMap map[string]*KVPair // Keypath -> KVPair for this layer
}

// KVPair represents a key-value pair with metadata
type KVPair struct {
	Key   []byte
	Value []byte
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

// Get retrieves a value from the layer stack
func (m *MemLayerManager) Get(key []byte) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Search from latest to oldest layer
	for i := len(m.layers) - 1; i >= 0; i-- {
		layer := m.layers[i]
		value, exists := layer.layerMap[string(key)]
		if exists {
			// If exists in this layer
			if value.Value != nil {
				return value.Value, nil
			}
			// Value is nil, which means it's deleted in this layer
			// Return nil immediately
			return nil, nil
		}
		// Key doesn't exist in this layer, continue to parent
	}

	// Not found in mem layers, query disk layer
	return m.diskLayer.Get(key)
}

// Set creates a new layer with the given changeset
func (m *MemLayerManager) Set(changeset []*KVPair) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	// Create new layer from the latest layer
	var parent *MemLayer
	if len(m.layers) > 0 {
		parent = m.layers[len(m.layers)-1]
	}
	
	layer := &MemLayer{
		id:        m.nextID,
		parent:    parent,
		createdAt: time.Now(),
		layerMap:  make(map[string]*KVPair),
	}
	
	// Build layer map
	for _, kv := range changeset {
		keyStr := string(kv.Key)
		if kv.Value == nil {
			// Deletion - store with nil value to indicate deletion
			layer.layerMap[keyStr] = &KVPair{
				Key:   CloneBytes(kv.Key),
				Value: nil, // nil value means deletion
			}
		} else {
			// Insert/Update
			layer.layerMap[keyStr] = &KVPair{
				Key:   CloneBytes(kv.Key),
				Value: CloneBytes(kv.Value),
			}
		}
	}
	
	// Build the tree for this layer
	layer.tree = m.buildLayerTree(layer)
	
	m.layers = append(m.layers, layer)
	m.nextID++
	
	// Check if compaction is needed
	if len(m.layers) > m.maxLayers {
		return m.compact()
	}
	
	return nil
}

// compact merges all mem layers into the disk layer
func (m *MemLayerManager) compact() error {
	if len(m.layers) == 0 {
		return nil
	}
	
	// Collect all changes from all layers
	allChanges := make(map[string]*KVPair)
	
	for _, layer := range m.layers {
		for key, kv := range layer.layerMap {
			allChanges[key] = kv
		}
	}
	
	// Convert to KVPair slice
	changes := make([]*KVPair, 0, len(allChanges))
	for _, kv := range allChanges {
		changes = append(changes, kv)
	}
	
	// Apply changes to disk layer
	if err := m.diskLayer.ApplyChanges(changes); err != nil {
		return err
	}
	
	// Keep only the latest layer after compaction
	// This allows querying recent history
	latestLayer := m.layers[len(m.layers)-1]
	// Create a new layer that references the disk layer
	newLayer := &MemLayer{
		id:        m.nextID,
		parent:    nil, // Points to disk layer
		createdAt: time.Now(),
		layerMap:  latestLayer.layerMap, // Keep recent changes
	}
	
	m.layers = []*MemLayer{newLayer}
	m.nextID++
	
	return nil
}

// buildLayerTree builds a tree from a layer and its parent
func (m *MemLayerManager) buildLayerTree(layer *MemLayer) *MutableTree {
	// Create a new mutable tree
	tree := &MutableTree{
		db:        m.diskLayer.db,
		logger:    m.diskLayer.logger,
		root:      nil,
		version:   0, // Mem layer version
		size:      0,
		height:    0,
		nodeCache: make(map[string]*Node),
		updates:   make(map[string]*Node),
	}

	// Start with parent's tree if exists
	if layer.parent != nil && layer.parent.tree != nil {
		tree.root = layer.parent.tree.root.clone()
		tree.size = layer.parent.tree.size
		tree.height = layer.parent.tree.height
	}

	// Apply this layer's changes
	for _, kv := range layer.layerMap {
		if kv.Value == nil {
			// Deletion
			tree.Remove(kv.Key)
		} else {
			// Insert/Update
			tree.Set(kv.Key, kv.Value)
		}
	}

	return tree
}

// GetVersion returns a snapshot of the tree at a specific layer version
func (m *MemLayerManager) GetVersion(version int64) (*MutableTree, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	// Find the layer
	for _, layer := range m.layers {
		if layer.id == version {
			// Return a copy of the layer's tree
			tree := layer.tree.clone()
			return tree, nil
		}
	}
	
	return nil, fmt.Errorf("version %d not found in mem layers", version)
}

// LatestVersion returns the latest mem layer version
func (m *MemLayerManager) LatestVersion() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	if len(m.layers) == 0 {
		return 0
	}
	return m.layers[len(m.layers)-1].id
}

// ListVersions returns all available versions (mem + disk)
func (m *MemLayerManager) ListVersions() ([]int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	versions := make([]int64, 0)
	
	// Add mem layer versions
	for _, layer := range m.layers {
		versions = append(versions, layer.id)
	}
	
	// Add disk layer versions
	diskVersions, err := m.diskLayer.ListVersions()
	if err != nil {
		return nil, err
	}
	versions = append(versions, diskVersions...)
	
	return versions, nil
}

// Get retrieves a value from a specific mem layer
func (l *MemLayer) Get(key []byte) ([]byte, error) {
	keyStr := string(key)

	// Check this layer first
	if kv, exists := l.layerMap[keyStr]; exists {
		return kv.Value, nil
	}

	// Check parent layer
	if l.parent != nil {
		return l.parent.Get(key)
	}

	return nil, nil
}

// clone creates a deep copy of the layer
func (l *MemLayer) clone() *MemLayer {
	newLayer := &MemLayer{
		id:        l.id,
		parent:    l.parent,
		createdAt: l.createdAt,
		layerMap:  make(map[string]*KVPair),
	}
	
	// Copy layer map
	for k, v := range l.layerMap {
		newLayer.layerMap[k] = &KVPair{
			Key:   CloneBytes(v.Key),
			Value: CloneBytes(v.Value),
		}
	}
	
	// Copy tree
	if l.tree != nil {
		newLayer.tree = l.tree.clone()
	}
	
	return newLayer
}

// DiskLayer represents the persistent path-based storage
type DiskLayer struct {
	db     *Database
	logger Logger
}

// NewDiskLayer creates a new disk layer
func NewDiskLayer(db corestore.KVStoreWithBatch, logger Logger) *DiskLayer {
	return &DiskLayer{
		db:     NewDatabase(db, logger),
		logger: logger,
	}
}

// Get retrieves a value from the disk layer
func (d *DiskLayer) Get(key []byte) ([]byte, error) {
	return d.db.LoadLatestState(key)
}

// ApplyChanges applies a changeset to the disk layer
func (d *DiskLayer) ApplyChanges(changes []*KVPair) error {
	for _, kv := range changes {
		if kv.Value == nil {
			// Deletion
			if err := d.db.DeleteLatestState(kv.Key); err != nil {
				return err
			}
		} else {
			// Insert/Update
			if err := d.db.SaveLatestState(kv.Key, kv.Value); err != nil {
				return err
			}
		}
	}
	return nil
}

// ListVersions lists all versions in the disk layer
func (d *DiskLayer) ListVersions() ([]int64, error) {
	return d.db.ListVersions()
}

// GetVersionMetadata retrieves metadata for a specific version
func (d *DiskLayer) GetVersionMetadata(version int64) (*VersionMetadata, error) {
	return d.db.LoadVersionMetadata(version)
}
