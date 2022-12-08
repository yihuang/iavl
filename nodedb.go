package iavl

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/pkg/errors"
	dbm "github.com/tendermint/tm-db"

	"github.com/cosmos/iavl/cache"
	"github.com/cosmos/iavl/internal/logger"
)

const (
	int64Size         = 8
	hashSize          = sha256.Size
	genesisVersion    = 1
	storageVersionKey = "storage_version"
	// We store latest saved version together with storage version delimited by the constant below.
	// This delimiter is valid only if fast storage is enabled (i.e. storageVersion >= fastStorageVersionValue).
	// The latest saved version is needed for protection against downgrade and re-upgrade. In such a case, it would
	// be possible to observe mismatch between the latest version state and the fast nodes on disk.
	// Therefore, we would like to detect that and overwrite fast nodes on disk with the latest version state.
	fastStorageVersionDelimiter = "-"
	// Using semantic versioning: https://semver.org/
	defaultStorageVersionValue = "1.0.0"
	fastStorageVersionValue    = "1.1.0"
	fastNodeCacheSize          = 100000
	maxVersion                 = int64(math.MaxInt64)
)

var (
	// All node keys are prefixed with the byte 'n'. This ensures no collision is
	// possible with the other keys, and makes them easier to traverse. They are indexed by the node hash.
	nodeKeyFormat = NewKeyFormat('n', hashSize) // n<hash>

	// Key Format for making reads and iterates go through a data-locality preserving db.
	// The value at an entry will list what version it was written to.
	// Then to query values, you first query state via this fast method.
	// If its present, then check the tree version. If tree version >= result_version,
	// return result_version. Else, go through old (slow) IAVL get method that walks through tree.
	fastKeyFormat = NewKeyFormat('f', 0) // f<keystring>

	// Key Format for storing metadata about the chain such as the vesion number.
	// The value at an entry will be in a variable format and up to the caller to
	// decide how to parse.
	metadataKeyFormat = NewKeyFormat('m', 0) // v<keystring>

	// Root nodes are indexed separately by their version
	rootKeyFormat = NewKeyFormat('r', int64Size) // r<version>
)

var (
	errInvalidFastStorageVersion = fmt.Sprintf("Fast storage version must be in the format <storage version>%s<latest fast cache version>", fastStorageVersionDelimiter)
)

type nodeDB struct {
	mtx            sync.Mutex       // Read/write lock.
	db             dbm.DB           // Persistent node storage.
	batch          dbm.Batch        // Batched writing buffer.
	opts           Options          // Options to customize for pruning/writing
	versionReaders map[int64]uint32 // Number of active version readers
	storageVersion string           // Storage version
	latestVersion  int64            // Latest version of nodeDB.
	nodeCache      cache.Cache      // Cache for nodes in the regular tree that consists of key-value pairs at any version.
	fastNodeCache  cache.Cache      // Cache for nodes in the fast index that represents only key-value pairs at the latest version.
}

func newNodeDB(db dbm.DB, cacheSize int, opts *Options) *nodeDB {
	if opts == nil {
		o := DefaultOptions()
		opts = &o
	}

	storeVersion, err := db.Get(metadataKeyFormat.Key(unsafeToBz(storageVersionKey)))

	if err != nil || storeVersion == nil {
		storeVersion = []byte(defaultStorageVersionValue)
	}

	return &nodeDB{
		db:             db,
		batch:          db.NewBatch(),
		opts:           *opts,
		latestVersion:  0, // initially invalid
		nodeCache:      cache.New(cacheSize),
		fastNodeCache:  cache.New(fastNodeCacheSize),
		versionReaders: make(map[int64]uint32, 8),
		storageVersion: string(storeVersion),
	}
}

// getNode assume the caller has hold the lock
func (ndb *nodeDB) getNode(hash []byte) (*Node, error) {
	if len(hash) == 0 {
		return nil, ErrNodeMissingHash
	}

	// Check the cache.
	if cachedNode := ndb.nodeCache.Get(hash); cachedNode != nil {
		ndb.opts.Stat.IncCacheHitCnt()
		return cachedNode.(*Node), nil
	}

	ndb.opts.Stat.IncCacheMissCnt()

	// Doesn't exist, load.
	buf, err := ndb.db.Get(ndb.nodeKey(hash))
	if err != nil {
		return nil, fmt.Errorf("can't get node %X: %v", hash, err)
	}
	if buf == nil {
		return nil, fmt.Errorf("Value missing for hash %x corresponding to nodeKey %x", hash, ndb.nodeKey(hash))
	}

	node, err := MakeNode(buf)
	if err != nil {
		return nil, fmt.Errorf("Error reading Node. bytes: %x, error: %v", buf, err)
	}

	node.hash = hash
	node.persisted = true
	ndb.nodeCache.Add(node)

	return node, nil
}

// GetNode gets a node from memory or disk. If it is an inner node, it does not
// load its children.
func (ndb *nodeDB) GetNode(hash []byte) (*Node, error) {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()
	return ndb.getNode(hash)
}

func (ndb *nodeDB) GetFastNode(key []byte) (*FastNode, error) {
	if !ndb.hasUpgradedToFastStorage() {
		return nil, errors.New("storage version is not fast")
	}

	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	if len(key) == 0 {
		return nil, fmt.Errorf("nodeDB.GetFastNode() requires key, len(key) equals 0")
	}

	if cachedFastNode := ndb.fastNodeCache.Get(key); cachedFastNode != nil {
		ndb.opts.Stat.IncFastCacheHitCnt()
		return cachedFastNode.(*FastNode), nil
	}

	ndb.opts.Stat.IncFastCacheMissCnt()

	// Doesn't exist, load.
	buf, err := ndb.db.Get(ndb.fastNodeKey(key))
	if err != nil {
		return nil, fmt.Errorf("can't get FastNode %X: %w", key, err)
	}
	if buf == nil {
		return nil, nil
	}

	fastNode, err := DeserializeFastNode(key, buf)
	if err != nil {
		return nil, fmt.Errorf("error reading FastNode. bytes: %x, error: %w", buf, err)
	}

	ndb.fastNodeCache.Add(fastNode)
	return fastNode, nil
}

// SaveNode saves a node to disk.
func (ndb *nodeDB) SaveNode(node *Node) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	if node.hash == nil {
		return ErrNodeMissingHash
	}
	if node.persisted {
		return ErrNodeAlreadyPersisted
	}

	// Save node bytes to db.
	var buf bytes.Buffer
	buf.Grow(node.encodedSize())

	if err := node.writeBytes(&buf); err != nil {
		return err
	}

	if err := ndb.batch.Set(ndb.nodeKey(node.hash), buf.Bytes()); err != nil {
		return err
	}
	logger.Debug("BATCH SAVE %X %p\n", node.hash, node)
	node.persisted = true
	ndb.nodeCache.Add(node)
	return nil
}

// SaveNode saves a FastNode to disk and add to cache.
func (ndb *nodeDB) SaveFastNode(node *FastNode) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()
	return ndb.saveFastNodeUnlocked(node, true)
}

// SaveNode saves a FastNode to disk without adding to cache.
func (ndb *nodeDB) SaveFastNodeNoCache(node *FastNode) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()
	return ndb.saveFastNodeUnlocked(node, false)
}

// setFastStorageVersionToBatch sets storage version to fast where the version is
// 1.1.0-<version of the current live state>. Returns error if storage version is incorrect or on
// db error, nil otherwise. Requires changes to be committed after to be persisted.
func (ndb *nodeDB) setFastStorageVersionToBatch() error {
	var newVersion string
	if ndb.storageVersion >= fastStorageVersionValue {
		// Storage version should be at index 0 and latest fast cache version at index 1
		versions := strings.Split(ndb.storageVersion, fastStorageVersionDelimiter)

		if len(versions) > 2 {
			return errors.New(errInvalidFastStorageVersion)
		}

		newVersion = versions[0]
	} else {
		newVersion = fastStorageVersionValue
	}

	latestVersion, err := ndb.getLatestVersion()
	if err != nil {
		return err
	}

	newVersion += fastStorageVersionDelimiter + strconv.Itoa(int(latestVersion))

	if err := ndb.batch.Set(metadataKeyFormat.Key([]byte(storageVersionKey)), []byte(newVersion)); err != nil {
		return err
	}
	ndb.storageVersion = newVersion
	return nil
}

func (ndb *nodeDB) getStorageVersion() string {
	return ndb.storageVersion
}

// Returns true if the upgrade to latest storage version has been performed, false otherwise.
func (ndb *nodeDB) hasUpgradedToFastStorage() bool {
	return ndb.getStorageVersion() >= fastStorageVersionValue
}

// Returns true if the upgrade to fast storage has occurred but it does not match the live state, false otherwise.
// When the live state is not matched, we must force reupgrade.
// We determine this by checking the version of the live state and the version of the live state when
// latest storage was updated on disk the last time.
func (ndb *nodeDB) shouldForceFastStorageUpgrade() (bool, error) {
	versions := strings.Split(ndb.storageVersion, fastStorageVersionDelimiter)

	if len(versions) == 2 {
		latestVersion, err := ndb.getLatestVersion()
		if err != nil {
			// TODO: should be true or false as default? (removed panic here)
			return false, err
		}
		if versions[1] != strconv.Itoa(int(latestVersion)) {
			return true, nil
		}
	}
	return false, nil
}

// SaveNode saves a FastNode to disk.
func (ndb *nodeDB) saveFastNodeUnlocked(node *FastNode, shouldAddToCache bool) error {
	if node.key == nil {
		return fmt.Errorf("cannot have FastNode with a nil value for key")
	}

	// Save node bytes to db.
	var buf bytes.Buffer
	buf.Grow(node.encodedSize())

	if err := node.writeBytes(&buf); err != nil {
		return fmt.Errorf("error while writing fastnode bytes. Err: %w", err)
	}

	if err := ndb.batch.Set(ndb.fastNodeKey(node.key), buf.Bytes()); err != nil {
		return fmt.Errorf("error while writing key/val to nodedb batch. Err: %w", err)
	}
	if shouldAddToCache {
		ndb.fastNodeCache.Add(node)
	}
	return nil
}

// Has checks if a hash exists in the database.
func (ndb *nodeDB) Has(hash []byte) (bool, error) {
	key := ndb.nodeKey(hash)

	if ldb, ok := ndb.db.(*dbm.GoLevelDB); ok {
		exists, err := ldb.DB().Has(key, nil)
		if err != nil {
			return false, err
		}
		return exists, nil
	}
	value, err := ndb.db.Get(key)
	if err != nil {
		return false, err
	}

	return value != nil, nil
}

// SaveBranch saves the given node and all of its descendants.
// NOTE: This function clears leftNode/rigthNode recursively and
// calls _hash() on the given node.
// TODO refactor, maybe use hashWithCount() but provide a callback.
func (ndb *nodeDB) SaveBranch(node *Node) ([]byte, error) {
	if node.persisted {
		return node.hash, nil
	}

	var err error
	if node.leftNode != nil {
		node.leftHash, err = ndb.SaveBranch(node.leftNode)
	}

	if err != nil {
		return nil, err
	}

	if node.rightNode != nil {
		node.rightHash, err = ndb.SaveBranch(node.rightNode)
	}

	if err != nil {
		return nil, err
	}

	_, err = node._hash()
	if err != nil {
		return nil, err
	}

	err = ndb.SaveNode(node)
	if err != nil {
		return nil, err
	}

	// resetBatch only working on generate a genesis block
	if node.version <= genesisVersion {
		if err = ndb.resetBatch(); err != nil {
			return nil, err
		}
	}
	node.leftNode = nil
	node.rightNode = nil

	return node.hash, nil
}

// resetBatch reset the db batch, keep low memory used
func (ndb *nodeDB) resetBatch() error {
	var err error
	if ndb.opts.Sync {
		err = ndb.batch.WriteSync()
	} else {
		err = ndb.batch.Write()
	}
	if err != nil {
		return err
	}
	err = ndb.batch.Close()
	if err != nil {
		return err
	}

	ndb.batch = ndb.db.NewBatch()

	return nil
}

// Contract:
// - successor must exists, predecessor can be 0 if don't exists.
// - predecessor < version < successor
// - no other versions between predecessor, version, successor
func (ndb *nodeDB) deleteVersion(version, predecessor, successor int64) error {
	if err := ndb.deleteOrphans(version, predecessor, successor); err != nil {
		return err
	}

	if err := ndb.batch.Delete(ndb.rootKey(version)); err != nil {
		return err
	}

	return nil
}

// DeleteVersion deletes a tree version from disk.
func (ndb *nodeDB) DeleteVersion(version int64, checkLatestVersion bool) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	if ndb.versionReaders[version] > 0 {
		return errors.Errorf("unable to delete version %v, it has %v active readers", version, ndb.versionReaders[version])
	}

	// Will be zero if there is no previous version.
	predecessor, err := ndb.getPreviousVersion(version)
	if err != nil {
		return err
	}

	// Will be zero if there is no next version.
	successor, err := ndb.getNextVersion(version)
	if err != nil {
		return err
	}

	// nothing to do for latest version
	if successor > 0 {
		if err := ndb.deleteVersion(version, predecessor, successor); err != nil {
			return err
		}
	}
	return nil
}

// rollbackVersion rollbacks the latest version,
// it traverse the tree to delete nodes that's created after the predecessor.
// Contract: version must be the latest version.
func (ndb *nodeDB) rollbackVersion(version, predecessor int64) error {
	rootHash, err := ndb.getRoot(version)
	if err != nil {
		return err
	}
	// preorder traversal with iteration
	var stack [][]byte
	if len(rootHash) > 0 {
		stack = append(stack, rootHash)
	}
	for len(stack) > 0 {
		hash := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		node, err := ndb.getNode(hash)
		if err != nil {
			return err
		}

		if node.version <= predecessor {
			continue
		}

		if err := ndb.batch.Delete(ndb.nodeKey(hash)); err != nil {
			return err
		}
		ndb.nodeCache.Remove(hash)

		if !node.isLeaf() {
			stack = append(stack, node.rightHash, node.leftHash)
		}
	}
	if err := ndb.batch.Delete(ndb.rootKey(version)); err != nil {
		return err
	}
	return nil
}

// DeleteVersionsFrom permanently deletes all tree versions from the given version upwards.
func (ndb *nodeDB) DeleteVersionsFrom(version int64) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	for v, r := range ndb.versionReaders {
		if v >= version && r != 0 {
			return errors.Errorf("unable to delete version %v with %v active readers", v, r)
		}
	}

	predecessor, err := ndb.getPreviousVersion(version)
	if err != nil {
		return err
	}

	// collect versions to delete
	var versions []int64
	if err := ndb.traverseRange(ndb.rootKey(version), ndb.rootKey(maxVersion), func(k, v []byte) error {
		var version int64
		rootKeyFormat.Scan(k, &version)
		versions = append(versions, version)
		return nil
	}); err != nil {
		return err
	}

	// delete versions backwardly
	for i := len(versions) - 1; i >= 0; i-- {
		pre := predecessor
		if i > 0 {
			pre = versions[i-1]
		}
		if err := ndb.rollbackVersion(versions[i], pre); err != nil {
			return err
		}
	}

	// NOTICE: we don't touch fast node indexes here, because it'll be rebuilt later because of version mismatch.

	return nil
}

// DeleteVersionsRange deletes versions from an interval (not inclusive).
func (ndb *nodeDB) DeleteVersionsRange(fromVersion, toVersion int64) error {
	if fromVersion >= toVersion {
		return errors.New("toVersion must be greater than fromVersion")
	}
	if toVersion == 0 {
		return errors.New("toVersion must be greater than 0")
	}

	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	predecessor, err := ndb.getPreviousVersion(fromVersion)
	if err != nil {
		return err
	}

	// The shared successor which the range of version will compare against.
	// Will be zero if there is no next version.
	successor, err := ndb.getNextVersion(toVersion - 1)
	if err != nil {
		return err
	}
	if successor == 0 {
		// no successor after `toVersion-1` means `latestVersion <= toVersion-1`
		return fmt.Errorf("cannot delete latest saved version (%d)", ndb.latestVersion)
	}

	for v, r := range ndb.versionReaders {
		if v < toVersion && v > predecessor && r != 0 {
			return errors.Errorf("unable to delete version %v with %v active readers", v, r)
		}
	}

	// collect the versions to delete
	var versions []int64
	if err := ndb.traverseRange(ndb.rootKey(fromVersion), ndb.rootKey(toVersion), func(k, v []byte) error {
		var version int64
		rootKeyFormat.Scan(k, &version)
		versions = append(versions, version)
		return nil
	}); err != nil {
		return err
	}

	// delete versions in reverse order
	for i := len(versions) - 1; i >= 0; i-- {
		pre := predecessor
		if i > 0 {
			pre = versions[i-1]
		}
		if err := ndb.deleteVersion(versions[i], pre, successor); err != nil {
			return err
		}
	}

	return nil
}

func (ndb *nodeDB) DeleteFastNode(key []byte) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()
	if err := ndb.batch.Delete(ndb.fastNodeKey(key)); err != nil {
		return err
	}
	ndb.fastNodeCache.Remove(key)
	return nil
}

// deleteOrphans deletes orphaned nodes from disk by comparing the related versions.
// Contract:
// - predecessor < version < successor
// - there's no other version in between
// - predecessor could be 0 if don't exists, successor must exist.
func (ndb *nodeDB) deleteOrphans(version, predecessor, successor int64) error {
	var (
		root1, root2 *Node
		err          error
	)

	rootHash1, err := ndb.getRoot(version)
	if err != nil {
		return err
	}
	rootHash2, err := ndb.getRoot(successor)
	if err != nil {
		return err
	}

	if len(rootHash1) > 0 {
		if root1, err = ndb.getNode(rootHash1); err != nil {
			return err
		}
	}
	if len(rootHash2) > 0 {
		if root2, err = ndb.getNode(rootHash2); err != nil {
			return err
		}
	}

	return DiffTree(ndb.getNode, root1, root2, func(orphaned, new []*Node) (bool, error) {
		for _, n := range orphaned {
			if n.version > predecessor {
				logger.Debug("DELETE predecessor:%v fromVersion:%v toVersion:%v %X\n", predecessor, n.version, version, n.hash)
				if err := ndb.batch.Delete(ndb.nodeKey(n.hash)); err != nil {
					return false, err
				}
				ndb.nodeCache.Remove(n.hash)
			}
		}
		return false, nil
	})
}

func (ndb *nodeDB) nodeKey(hash []byte) []byte {
	return nodeKeyFormat.KeyBytes(hash)
}

func (ndb *nodeDB) fastNodeKey(key []byte) []byte {
	return fastKeyFormat.KeyBytes(key)
}

func (ndb *nodeDB) rootKey(version int64) []byte {
	return rootKeyFormat.Key(version)
}

func (ndb *nodeDB) getLatestVersion() (int64, error) {
	if ndb.latestVersion == 0 {
		var err error
		ndb.latestVersion, err = ndb.getPreviousVersion(maxVersion)
		if err != nil {
			return 0, err
		}
	}
	return ndb.latestVersion, nil
}

func (ndb *nodeDB) updateLatestVersion(version int64) {
	if ndb.latestVersion < version {
		ndb.latestVersion = version
	}
}

func (ndb *nodeDB) resetLatestVersion(version int64) {
	ndb.latestVersion = version
}

// getPreviousVersion returns 0 if there's no previous version
func (ndb *nodeDB) getPreviousVersion(version int64) (int64, error) {
	itr, err := ndb.db.ReverseIterator(
		rootKeyFormat.Key(1),
		rootKeyFormat.Key(version),
	)
	if err != nil {
		return 0, err
	}
	defer itr.Close()

	pversion := int64(-1)
	for ; itr.Valid(); itr.Next() {
		k := itr.Key()
		rootKeyFormat.Scan(k, &pversion)
		return pversion, nil
	}

	if err := itr.Error(); err != nil {
		return 0, err
	}

	return 0, nil
}

// firstVersion returns the first version in the iavl tree, returns 0 if it's empty.
func (ndb *nodeDB) firstVersion() (int64, error) {
	itr, err := dbm.IteratePrefix(ndb.db, rootKeyFormat.Key())
	if err != nil {
		return 0, err
	}
	defer itr.Close()
	if itr.Valid() {
		var version int64
		rootKeyFormat.Scan(itr.Key(), &version)
		return version, nil
	}
	return 0, nil
}

// getNextVersion returns 0 if there's no next version
func (ndb *nodeDB) getNextVersion(version int64) (int64, error) {
	itr, err := ndb.db.Iterator(
		rootKeyFormat.Key(version+1),
		rootKeyFormat.Key(maxVersion),
	)
	if err != nil {
		return 0, err
	}
	defer itr.Close()

	if itr.Valid() {
		k := itr.Key()
		var nversion int64
		rootKeyFormat.Scan(k, &nversion)
		return nversion, nil
	}

	if err := itr.Error(); err != nil {
		return 0, err
	}

	return 0, nil
}

// Traverse fast nodes and return error if any, nil otherwise
// nolint: unused
func (ndb *nodeDB) traverseFastNodes(fn func(k, v []byte) error) error {
	return ndb.traversePrefix(fastKeyFormat.Key(), fn)
}

// Traverse all keys and return error if any, nil otherwise
// nolint: unused
func (ndb *nodeDB) traverse(fn func(key, value []byte) error) error {
	return ndb.traverseRange(nil, nil, fn)
}

// Traverse all keys between a given range (excluding end) and return error if any, nil otherwise
func (ndb *nodeDB) traverseRange(start []byte, end []byte, fn func(k, v []byte) error) error {
	itr, err := ndb.db.Iterator(start, end)
	if err != nil {
		return err
	}
	defer itr.Close()

	for ; itr.Valid(); itr.Next() {
		if err := fn(itr.Key(), itr.Value()); err != nil {
			return err
		}
	}

	if err := itr.Error(); err != nil {
		return err
	}

	return nil
}

// Traverse all keys with a certain prefix. Return error if any, nil otherwise
func (ndb *nodeDB) traversePrefix(prefix []byte, fn func(k, v []byte) error) error {
	itr, err := dbm.IteratePrefix(ndb.db, prefix)
	if err != nil {
		return err
	}
	defer itr.Close()

	for ; itr.Valid(); itr.Next() {
		if err := fn(itr.Key(), itr.Value()); err != nil {
			return err
		}
	}

	return nil
}

// Get iterator for fast prefix and error, if any
func (ndb *nodeDB) getFastIterator(start, end []byte, ascending bool) (dbm.Iterator, error) {
	var startFormatted, endFormatted []byte

	if start != nil {
		startFormatted = fastKeyFormat.KeyBytes(start)
	} else {
		startFormatted = fastKeyFormat.Key()
	}

	if end != nil {
		endFormatted = fastKeyFormat.KeyBytes(end)
	} else {
		endFormatted = fastKeyFormat.Key()
		endFormatted[0]++
	}

	if ascending {
		return ndb.db.Iterator(startFormatted, endFormatted)
	}

	return ndb.db.ReverseIterator(startFormatted, endFormatted)
}

// Write to disk.
func (ndb *nodeDB) Commit() error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	var err error
	if ndb.opts.Sync {
		err = ndb.batch.WriteSync()
	} else {
		err = ndb.batch.Write()
	}
	if err != nil {
		return errors.Wrap(err, "failed to write batch")
	}

	ndb.batch.Close()
	ndb.batch = ndb.db.NewBatch()

	return nil
}

func (ndb *nodeDB) HasRoot(version int64) (bool, error) {
	return ndb.db.Has(ndb.rootKey(version))
}

func (ndb *nodeDB) getRoot(version int64) ([]byte, error) {
	return ndb.db.Get(ndb.rootKey(version))
}

func (ndb *nodeDB) getRoots() (roots map[int64][]byte, err error) {
	roots = make(map[int64][]byte)
	err = ndb.traversePrefix(rootKeyFormat.Key(), func(k, v []byte) error {
		var version int64
		rootKeyFormat.Scan(k, &version)
		roots[version] = v
		return nil
	})
	return roots, err
}

// SaveRoot creates an entry on disk for the given root, so that it can be
// loaded later.
func (ndb *nodeDB) SaveRoot(root *Node, version int64) error {
	if len(root.hash) == 0 {
		return ErrRootMissingHash
	}
	return ndb.saveRoot(root.hash, version)
}

// SaveEmptyRoot creates an entry on disk for an empty root.
func (ndb *nodeDB) SaveEmptyRoot(version int64) error {
	return ndb.saveRoot([]byte{}, version)
}

func (ndb *nodeDB) saveRoot(hash []byte, version int64) error {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()

	// We allow the initial version to be arbitrary
	latest, err := ndb.getLatestVersion()
	if err != nil {
		return err
	}
	if latest > 0 && version != latest+1 {
		return fmt.Errorf("must save consecutive versions; expected %d, got %d", latest+1, version)
	}

	if err := ndb.batch.Set(ndb.rootKey(version), hash); err != nil {
		return err
	}

	ndb.updateLatestVersion(version)

	return nil
}

func (ndb *nodeDB) incrVersionReaders(version int64) {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()
	ndb.versionReaders[version]++
}

func (ndb *nodeDB) decrVersionReaders(version int64) {
	ndb.mtx.Lock()
	defer ndb.mtx.Unlock()
	if ndb.versionReaders[version] > 0 {
		ndb.versionReaders[version]--
	}
}

// Utility and test functions

// nolint: unused
func (ndb *nodeDB) leafNodes() ([]*Node, error) {
	leaves := []*Node{}

	err := ndb.traverseNodes(func(hash []byte, node *Node) error {
		if node.isLeaf() {
			leaves = append(leaves, node)
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return leaves, nil
}

// nolint: unused
func (ndb *nodeDB) nodes() ([]*Node, error) {
	nodes := []*Node{}

	err := ndb.traverseNodes(func(hash []byte, node *Node) error {
		nodes = append(nodes, node)
		return nil
	})

	if err != nil {
		return nil, err
	}

	return nodes, nil
}

// Not efficient.
// NOTE: DB cannot implement Size() because
// mutations are not always synchronous.
//nolint:unused
func (ndb *nodeDB) size() int {
	size := 0
	err := ndb.traverse(func(k, v []byte) error {
		size++
		return nil
	})

	if err != nil {
		return -1
	}
	return size
}

func (ndb *nodeDB) traverseNodes(fn func(hash []byte, node *Node) error) error {
	nodes := []*Node{}

	err := ndb.traversePrefix(nodeKeyFormat.Key(), func(key, value []byte) error {
		node, err := MakeNode(value)
		if err != nil {
			return err
		}
		nodeKeyFormat.Scan(key, &node.hash)
		nodes = append(nodes, node)
		return nil
	})

	if err != nil {
		return err
	}

	sort.Slice(nodes, func(i, j int) bool {
		return bytes.Compare(nodes[i].key, nodes[j].key) < 0
	})

	for _, n := range nodes {
		if err := fn(n.hash, n); err != nil {
			return err
		}
	}
	return nil
}

func (ndb *nodeDB) String() (string, error) {
	buf := bufPool.Get().(*bytes.Buffer)
	defer bufPool.Put(buf)
	buf.Reset()

	index := 0

	err := ndb.traversePrefix(rootKeyFormat.Key(), func(key, value []byte) error {
		fmt.Fprintf(buf, "%s: %x\n", key, value)
		return nil
	})

	if err != nil {
		return "", err
	}

	buf.WriteByte('\n')

	err = ndb.traverseNodes(func(hash []byte, node *Node) error {
		switch {
		case len(hash) == 0:
			buf.WriteByte('\n')
		case node == nil:
			fmt.Fprintf(buf, "%s%40x: <nil>\n", nodeKeyFormat.Prefix(), hash)
		case node.value == nil && node.height > 0:
			fmt.Fprintf(buf, "%s%40x: %s   %-16s h=%d version=%d\n",
				nodeKeyFormat.Prefix(), hash, node.key, "", node.height, node.version)
		default:
			fmt.Fprintf(buf, "%s%40x: %s = %-16s h=%d version=%d\n",
				nodeKeyFormat.Prefix(), hash, node.key, node.value, node.height, node.version)
		}
		index++
		return nil
	})

	if err != nil {
		return "", err
	}

	return "-" + "\n" + buf.String() + "-", nil
}

var (
	ErrNodeMissingHash      = fmt.Errorf("node does not have a hash")
	ErrNodeAlreadyPersisted = fmt.Errorf("shouldn't be calling save on an already persisted node")
	ErrRootMissingHash      = fmt.Errorf("root hash must not be empty")
)
