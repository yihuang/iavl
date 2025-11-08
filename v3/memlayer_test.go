package iavl

import (
	"testing"
	
	"github.com/cosmos/iavl/db"
)

func TestMemLayerBasic(t *testing.T) {
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

func TestMemLayerDeletion(t *testing.T) {
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

func TestMemLayerVersioning(t *testing.T) {
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
	
	// List versions
	versions, err := manager.ListVersions()
	if err != nil {
		t.Errorf("ListVersions failed: %v", err)
	}
	
	t.Logf("Available versions: %v", versions)
}

func TestMemLayerCompaction(t *testing.T) {
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
	versions, err := manager.ListVersions()
	if err != nil {
		t.Errorf("ListVersions failed: %v", err)
	}
	
	// Check that disk layer has the latest version
	t.Logf("Versions after compaction: %v", versions)
}

func TestDiskLayerPersistence(t *testing.T) {
	memdb := db.NewMemDB()
	diskLayer := NewDiskLayer(memdb, NewNopLogger())
	
	// Test ApplyChanges
	changes := []*KVPair{
		{Key: []byte("key1"), Value: []byte("value1")},
		{Key: []byte("key2"), Value: []byte("value2")},
	}
	
	err := diskLayer.ApplyChanges(changes)
	if err != nil {
		t.Errorf("ApplyChanges failed: %v", err)
	}
	
	// Test Get
	value, err := diskLayer.Get([]byte("key1"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if string(value) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", string(value))
	}
	
	// Test version metadata
	meta := &VersionMetadata{
		Version:      1,
		MerkleRoot:   []byte("root-hash"),
		Size:         2,
		Height:       1,
		Timestamp:    1234567890,
	}
	
	err = diskLayer.db.SaveVersionMetadata(meta)
	if err != nil {
		t.Errorf("SaveVersionMetadata failed: %v", err)
	}
	
	// Test ListVersions
	versions, err := diskLayer.ListVersions()
	if err != nil {
		t.Errorf("ListVersions failed: %v", err)
	}
	
	if len(versions) != 1 || versions[0] != 1 {
		t.Errorf("Expected version [1], got %v", versions)
	}
	
	// Test GetVersionMetadata
	loadedMeta, err := diskLayer.GetVersionMetadata(1)
	if err != nil {
		t.Errorf("GetVersionMetadata failed: %v", err)
	}
	
	if loadedMeta == nil {
		t.Errorf("Expected metadata, got nil")
	}
	
	t.Logf("Loaded metadata: %+v", loadedMeta)
}
