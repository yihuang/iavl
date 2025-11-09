package iavl

import (
	"testing"
	
	"github.com/cosmos/iavl/db"
)

func TestMemLayerBasicV2(t *testing.T) {
	// Create in-memory database
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	manager := NewMemLayerManager(diskLayer, 1024)
	
	// Create first layer
	changes1 := []*KVPair{
		{Key: []byte("key1"), Value: []byte("value1")},
		{Key: []byte("key2"), Value: []byte("value2")},
	}
	
	err := manager.Set(changes1)
	if err != nil {
		t.Errorf("Set layer 1 failed: %v", err)
	}
	
	// Query should find the values
	value, err := manager.Get([]byte("key1"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if string(value) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", string(value))
	}
	
	// Create second layer with updates
	changes2 := []*KVPair{
		{Key: []byte("key1"), Value: []byte("value1-updated")},
		{Key: []byte("key3"), Value: []byte("value3")},
	}
	
	err = manager.Set(changes2)
	if err != nil {
		t.Errorf("Set layer 2 failed: %v", err)
	}
	
	// Query should find updated value
	value, err = manager.Get([]byte("key1"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if string(value) != "value1-updated" {
		t.Errorf("Expected 'value1-updated', got '%s'", string(value))
	}
	
	// Query should find new value
	value, err = manager.Get([]byte("key3"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if string(value) != "value3" {
		t.Errorf("Expected 'value3', got '%s'", string(value))
	}
}

func TestMemLayerDeletionV2(t *testing.T) {
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	manager := NewMemLayerManager(diskLayer, 1024)
	
	// Create initial layer
	changes1 := []*KVPair{
		{Key: []byte("key1"), Value: []byte("value1")},
		{Key: []byte("key2"), Value: []byte("value2")},
	}
	manager.Set(changes1)
	
	// Delete in next layer
	changes2 := []*KVPair{
		{Key: []byte("key1"), Value: nil}, // Deletion
	}
	manager.Set(changes2)
	
	// Query should not find key1
	value, err := manager.Get([]byte("key1"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if value != nil {
		t.Errorf("Expected nil, got '%s'", string(value))
	}
	
	// key2 should still exist
	value, err = manager.Get([]byte("key2"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if string(value) != "value2" {
		t.Errorf("Expected 'value2', got '%s'", string(value))
	}
}

func TestMemLayerVersioningV2(t *testing.T) {
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	manager := NewMemLayerManager(diskLayer, 1024)
	
	// Create three layers
	changes1 := []*KVPair{{Key: []byte("key1"), Value: []byte("v1")}}
	manager.Set(changes1)
	version1 := manager.LatestVersion()
	
	changes2 := []*KVPair{{Key: []byte("key1"), Value: []byte("v2")}}
	manager.Set(changes2)
	version2 := manager.LatestVersion()
	
	changes3 := []*KVPair{{Key: []byte("key1"), Value: []byte("v3")}}
	manager.Set(changes3)
	version3 := manager.LatestVersion()
	
	if version2 <= version1 {
		t.Errorf("Version should increase")
	}
	if version3 <= version2 {
		t.Errorf("Version should increase")
	}
	
	// Get specific version
	tree, err := manager.GetVersion(version2)
	if err != nil {
		t.Errorf("GetVersion failed: %v", err)
	}
	if tree == nil {
		t.Errorf("Expected tree, got nil")
	}
	
	t.Logf("Version 1: %d, Version 2: %d, Version 3: %d", version1, version2, version3)
}

func TestMemLayerCompactionV2(t *testing.T) {
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	manager := NewMemLayerManager(diskLayer, 3) // Small max layers for testing
	
	// Create more layers than max
	for i := 0; i < 5; i++ {
		changes := []*KVPair{
			{Key: []byte("key1"), Value: []byte(string(rune('a' + i)))},
		}
		err := manager.Set(changes)
		if err != nil {
			t.Errorf("Set layer %d failed: %v", i, err)
		}
	}
	
	// After 5 layers with max 3, compaction should have occurred
	latestVersion := manager.LatestVersion()
	
	// The latest version should be from the compaction
	t.Logf("Latest version after compaction: %d", latestVersion)
	
	// Check that we can still query
	value, _ := manager.Get([]byte("key1"))
	if value == nil {
		t.Errorf("Expected value after compaction, got nil")
	}
}

func TestNodeCopyOnWrite(t *testing.T) {
	// Test that immutable nodes are properly copied
	key1 := []byte("key1")
	value1 := []byte("value1")
	
	// Create initial node
	node1 := NewNode(key1, value1).markImmutable()
	
	if !node1.isImmutable {
		t.Errorf("Expected node to be immutable")
	}
	
	// Try to modify - should create new node
	node2 := node1.insert([]byte("key2"), []byte("value2"))
	
	// node1 should remain immutable
	if !node1.isImmutable {
		t.Errorf("Original node should still be immutable")
	}
	
	// node2 should be different
	if node1 == node2 {
		t.Errorf("Should create new node")
	}
	
	// Original node should not have the new key
	if node1.get([]byte("key2")) != nil {
		t.Errorf("Original node should not have key2")
	}
	
	// New node should have the new key
	if string(node2.get([]byte("key2"))) != "value2" {
		t.Errorf("New node should have key2")
	}
}

func TestStructuralSharing(t *testing.T) {
	// Create initial tree
	var root *Node
	root = root.insert([]byte("key1"), []byte("value1")).markImmutable()
	root = root.insert([]byte("key2"), []byte("value2")).markImmutable()
	root = root.insert([]byte("key3"), []byte("value3")).markImmutable()

	// Create new layer by updating
	changes := []*KVPair{{Key: []byte("key1"), Value: []byte("new-value")}}
	newRoot := root.update(changes).markImmutable()

	// New root should be different
	if newRoot == root {
		t.Errorf("Should create new root")
	}

	// New root should have new value
	if string(newRoot.get([]byte("key1"))) != "new-value" {
		t.Errorf("New root should have new value")
	}

	// Unchanged keys should still be accessible
	if string(newRoot.get([]byte("key2"))) != "value2" {
		t.Errorf("New root should have key2")
	}
	if string(newRoot.get([]byte("key3"))) != "value3" {
		t.Errorf("New root should have key3")
	}
}

func TestLayeredIterator(t *testing.T) {
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	manager := NewMemLayerManager(diskLayer, 1024)

	// Create initial layer with multiple keys
	changes1 := []*KVPair{
		{Key: []byte("aaa"), Value: []byte("value1")},
		{Key: []byte("bbb"), Value: []byte("value2")},
		{Key: []byte("ccc"), Value: []byte("value3")},
	}
	manager.Set(changes1)

	// Create second layer with updates and new key
	changes2 := []*KVPair{
		{Key: []byte("bbb"), Value: []byte("value2-updated")},
		{Key: []byte("ddd"), Value: []byte("value4")},
	}
	manager.Set(changes2)

	// Test iteration
	iter, err := manager.Iterator(nil, nil)
	if err != nil {
		t.Errorf("Iterator creation failed: %v", err)
	}

	// Collect all keys
	keys := make([]string, 0)
	values := make([]string, 0)
	for iter.Valid() {
		keys = append(keys, string(iter.Key()))
		values = append(values, string(iter.Value()))
		iter.Next()
	}

	// Verify we got all expected keys
	expectedKeys := []string{"aaa", "bbb", "ccc", "ddd"}
	if len(keys) != len(expectedKeys) {
		t.Errorf("Expected %d keys, got %d", len(expectedKeys), len(keys))
	}

	for i, expectedKey := range expectedKeys {
		if i >= len(keys) || keys[i] != expectedKey {
			t.Errorf("Expected key %s at position %d, got %v", expectedKey, i, keys)
		}
	}

	// Verify updated value
	bbbIdx := -1
	for i, k := range keys {
		if k == "bbb" {
			bbbIdx = i
			break
		}
	}
	if bbbIdx == -1 {
		t.Errorf("bbb key not found")
	} else if values[bbbIdx] != "value2-updated" {
		t.Errorf("Expected 'value2-updated' for bbb, got '%s'", values[bbbIdx])
	}

	iter.Close()
}

func TestLayeredIteratorWithDeletion(t *testing.T) {
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	manager := NewMemLayerManager(diskLayer, 1024)

	// Create initial layer
	changes1 := []*KVPair{
		{Key: []byte("key1"), Value: []byte("value1")},
		{Key: []byte("key2"), Value: []byte("value2")},
		{Key: []byte("key3"), Value: []byte("value3")},
	}
	manager.Set(changes1)

	// Delete key2 in next layer
	changes2 := []*KVPair{
		{Key: []byte("key2"), Value: nil}, // Deletion
	}
	manager.Set(changes2)

	// Test iteration - should not include key2
	iter, err := manager.Iterator(nil, nil)
	if err != nil {
		t.Errorf("Iterator creation failed: %v", err)
	}

	keys := make([]string, 0)
	for iter.Valid() {
		keys = append(keys, string(iter.Key()))
		iter.Next()
	}

	// Verify key2 was not included
	for _, key := range keys {
		if key == "key2" {
			t.Errorf("key2 should have been deleted from iteration")
		}
	}

	// Verify we have key1 and key3
	if !contains(keys, "key1") {
		t.Errorf("key1 should be present")
	}
	if !contains(keys, "key3") {
		t.Errorf("key3 should be present")
	}

	iter.Close()
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
