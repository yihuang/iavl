package iavl

import (
	"bytes"
	"encoding/binary"

	corestore "cosmossdk.io/core/store"
)

// Database storage layer with path-based keys
type Database struct {
	db     corestore.KVStoreWithBatch
	logger Logger
}

// NewDatabase creates a new database instance
func NewDatabase(db corestore.KVStoreWithBatch, logger Logger) *Database {
	return &Database{
		db:     db,
		logger: logger,
	}
}

// Key formats:
// - 'n' + keypath: Tree nodes (internal and leaf nodes)
// - 'm' + version: Version metadata
// - 's' + key: Latest state (for O(1) access)

const (
	nodePrefix         byte = 'n'
	metadataPrefix     byte = 'm'
	latestStatePrefix  byte = 's'
)

// nodeKey creates a database key for a tree node
func (d *Database) nodeKey(keypath []byte) []byte {
	key := make([]byte, 0, 1+len(keypath))
	key = append(key, nodePrefix)
	key = append(key, keypath...)
	return key
}

// metadataKey creates a database key for version metadata
func (d *Database) metadataKey(version int64) []byte {
	key := make([]byte, 1+8)
	key[0] = metadataPrefix
	binary.BigEndian.PutUint64(key[1:], uint64(version))
	return key
}

// latestStateKey creates a database key for latest state
func (d *Database) latestStateKey(key []byte) []byte {
	k := make([]byte, 0, 1+len(key))
	k = append(k, latestStatePrefix)
	k = append(k, key...)
	return k
}

// SaveNode saves a node to the database
func (d *Database) SaveNode(keypath []byte, node *Node) error {
	data := node.serialize()
	return d.db.Set(d.nodeKey(keypath), data)
}

// LoadNode loads a node from the database
func (d *Database) LoadNode(keypath []byte) (*Node, error) {
	data, err := d.db.Get(d.nodeKey(keypath))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return deserializeNode(data)
}

// DeleteNode deletes a node from the database
func (d *Database) DeleteNode(keypath []byte) error {
	return d.db.Delete(d.nodeKey(keypath))
}

// SaveLatestState saves a key-value pair to the latest state
func (d *Database) SaveLatestState(key, value []byte) error {
	return d.db.Set(d.latestStateKey(key), value)
}

// LoadLatestState loads a value from the latest state
func (d *Database) LoadLatestState(key []byte) ([]byte, error) {
	return d.db.Get(d.latestStateKey(key))
}

// DeleteLatestState deletes a key from the latest state
func (d *Database) DeleteLatestState(key []byte) error {
	return d.db.Delete(d.latestStateKey(key))
}

// VersionMetadata holds information about a saved version
type VersionMetadata struct {
	Version      int64
	MerkleRoot   []byte
	Size         int64
	Height       int8
	Timestamp    int64
}

// SaveVersionMetadata saves version metadata
func (d *Database) SaveVersionMetadata(meta *VersionMetadata) error {
	var buf bytes.Buffer
	
	// Write version
	binary.Write(&buf, binary.BigEndian, meta.Version)
	
	// Write merkle root length and data
	binary.Write(&buf, binary.BigEndian, int64(len(meta.MerkleRoot)))
	buf.Write(meta.MerkleRoot)
	
	// Write size
	binary.Write(&buf, binary.BigEndian, meta.Size)
	
	// Write height
	binary.Write(&buf, binary.BigEndian, meta.Height)
	
	// Write timestamp
	binary.Write(&buf, binary.BigEndian, meta.Timestamp)
	
	return d.db.Set(d.metadataKey(meta.Version), buf.Bytes())
}

// LoadVersionMetadata loads version metadata
func (d *Database) LoadVersionMetadata(version int64) (*VersionMetadata, error) {
	data, err := d.db.Get(d.metadataKey(version))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	
	buf := bytes.NewBuffer(data)
	
	var v int64
	if err := binary.Read(buf, binary.BigEndian, &v); err != nil {
		return nil, err
	}
	
	var merkleRootLen int64
	if err := binary.Read(buf, binary.BigEndian, &merkleRootLen); err != nil {
		return nil, err
	}
	merkleRoot := make([]byte, merkleRootLen)
	if _, err := buf.Read(merkleRoot); err != nil {
		return nil, err
	}
	
	var size int64
	if err := binary.Read(buf, binary.BigEndian, &size); err != nil {
		return nil, err
	}
	
	var height int8
	if err := binary.Read(buf, binary.BigEndian, &height); err != nil {
		return nil, err
	}
	
	var timestamp int64
	if err := binary.Read(buf, binary.BigEndian, &timestamp); err != nil {
		return nil, err
	}
	
	return &VersionMetadata{
		Version:      v,
		MerkleRoot:   merkleRoot,
		Size:         size,
		Height:       height,
		Timestamp:    timestamp,
	}, nil
}

// DeleteVersionMetadata deletes version metadata
func (d *Database) DeleteVersionMetadata(version int64) error {
	return d.db.Delete(d.metadataKey(version))
}

// Iterator over nodes
func (d *Database) Iterator() (corestore.Iterator, error) {
	start := []byte{nodePrefix}
	end := []byte{nodePrefix + 1}
	return d.db.Iterator(start, end)
}

// Iterator over latest state
func (d *Database) LatestStateIterator() (corestore.Iterator, error) {
	start := []byte{latestStatePrefix}
	end := []byte{latestStatePrefix + 1}
	return d.db.Iterator(start, end)
}

// Iterator over metadata
func (d *Database) MetadataIterator() (corestore.Iterator, error) {
	start := []byte{metadataPrefix}
	end := []byte{metadataPrefix + 1}
	return d.db.Iterator(start, end)
}

// Commit commits the batch
func (d *Database) Commit() error {
	// KVStoreWithBatch doesn't have Commit, it's on the batch
	return nil
}

// NewBatch creates a new batch for atomic writes
func (d *Database) NewBatch() corestore.Batch {
	return d.db.NewBatch()
}

// ListVersions returns all available versions
func (d *Database) ListVersions() ([]int64, error) {
	itr, err := d.MetadataIterator()
	if err != nil {
		return nil, err
	}
	defer itr.Close()
	
	versions := make([]int64, 0)
	for ; itr.Valid(); itr.Next() {
		// Extract version from key
		if len(itr.Key()) == 9 && itr.Key()[0] == metadataPrefix {
			v := int64(binary.BigEndian.Uint64(itr.Key()[1:]))
			versions = append(versions, v)
		}
	}
	
	return versions, nil
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

// GetTreeRoot returns the root node of the tree from disk
// For simplicity, we rebuild the tree from latest state
func (d *DiskLayer) GetTreeRoot() (*Node, error) {
	// Get all latest state entries
	itr, err := d.db.LatestStateIterator()
	if err != nil {
		return nil, err
	}
	defer itr.Close()

	// Build tree from all key-value pairs
	var root *Node
	for ; itr.Valid(); itr.Next() {
		key := itr.Key()[1:] // Remove prefix
		value := itr.Value()
		root = root.insert(key, value)
	}

	return root, nil
}

// BuildNodeFromStorage builds a node from storage for a specific key
func (d *DiskLayer) BuildNodeFromStorage(changes []*KVPair) *Node {
	// For this implementation, we just build nodes from changes
	// In a real implementation, this would read from disk
	var root *Node
	for _, kv := range changes {
		if kv.Value != nil {
			root = root.insert(kv.Key, kv.Value)
		}
	}
	return root
}
