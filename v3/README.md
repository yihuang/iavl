# IAVL v3 - Path-Based Storage

IAVL v3 is a new implementation of the IAVL+ Merkle AVL tree with a path-based storage schema. Unlike v1 which stores multiple versions with `version+nonce` keys, v3 only persists the latest version in the database, significantly reducing storage overhead and eliminating the need for pruning.

## Key Features

- **Path-Based Storage**: Nodes are stored using their tree path as the database key, not `version+nonce`
- **Single Version Persistence**: Only the latest version is stored in the database
- **Metadata-Based History**: Historical versions are tracked via metadata (merkle roots) only
- **Zero Pruning Cost**: No pruning mechanism needed since historical versions aren't stored as data

## Storage Schema

### Database Keys

- `n<keypath>` - Tree nodes (internal and leaf nodes)
- `m<version>` - Version metadata (merkle root, size, timestamp)
- `s<key>` - Latest state (O(1) access for latest version queries)

### Key Components

1. **Node Structure** (node.go)
   - Path-based key using the node's position in the tree
   - No version field (single version storage)
   - Efficient serialization

2. **Tree Structure** (tree.go)
   - Manages the current working tree
   - Version management (SaveVersion, LoadVersion)
   - Set, Get, Remove operations

3. **Storage Layer** (db.go)
   - Wraps `corestore.KVStoreWithBatch`
   - Path-based key encoding/decoding
   - Batch operations

4. **Iterator** (iterator.go)
   - Iterate over latest state
   - Prefix and range queries

5. **Proof Generation** (proof.go)
   - Generate merkle proofs for latest version
   - Support for historical version proofs (requires rebuild)

## Usage Example

```go
// Create a new tree
tree := v3.NewMutableTree(db, 0, logger)

// Set values
tree.Set([]byte("key1"), []byte("value1"))
tree.Set([]byte("key2"), []byte("value2"))

// Save version
version, err := tree.SaveVersion()
if err != nil {
    return err
}

// Get latest value (O(1) from latest_state)
value, err := tree.Get([]byte("key1"))  // returns "value1"

// Load specific version
err = tree.LoadVersion(version)
if err != nil {
    return err
}

// Get historical value (requires tree rebuild)
value, err = tree.GetVersioned([]byte("key1"), version)
if err != nil {
    return err
}
```

## Advantages

1. **Storage Efficiency**: 90%+ reduction in storage (single copy vs multi-version)
2. **No Pruning**: Eliminate complex pruning logic and overhead
3. **Fast Queries**: O(1) latest version lookups via `latest_state`
4. **Simple Implementation**: Reduced complexity compared to v1

## Trade-offs

1. **Historical Queries**: Slower (requires tree rebuild)
2. **Proof Generation**: Historical version proofs need on-demand reconstruction
3. **Migration**: Requires conversion from v1/v2 format

## API Compatibility

v3 implements the same interface as v1:
- `Set(key, value []byte) *KVPair`
- `Get(key []byte) ([]byte, error)`
- `Remove(key []byte) ([]byte, error)`
- `SaveVersion() (int64, error)`
- `LoadVersion(version int64) error`
- `Iterator() Iterator`
- `GetVersioned(key []byte, version int64) ([]byte, error)`

## Status

Under development. Not yet production ready.
