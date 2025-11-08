package iavl

import (
	"testing"
	
	"github.com/cosmos/iavl/db"
)

func TestBasicOperations(t *testing.T) {
	// Create in-memory database
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Test empty tree
	if tree.Size() != 0 {
		t.Errorf("Expected size 0, got %d", tree.Size())
	}
	
	// Test Set
	err := tree.Set([]byte("key1"), []byte("value1"))
	if err != nil {
		t.Errorf("Set failed: %v", err)
	}
	
	// Test Get
	value, err := tree.Get([]byte("key1"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if string(value) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", string(value))
	}
	
	// Test multiple Set
	tree.Set([]byte("key2"), []byte("value2"))
	tree.Set([]byte("key3"), []byte("value3"))
	
	if tree.Size() != 3 {
		t.Errorf("Expected size 3, got %d", tree.Size())
	}
	
	// Test Get non-existent key
	value, err = tree.Get([]byte("key4"))
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if value != nil {
		t.Errorf("Expected nil, got '%s'", string(value))
	}
}

func TestRemove(t *testing.T) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Add some data
	tree.Set([]byte("key1"), []byte("value1"))
	tree.Set([]byte("key2"), []byte("value2"))
	tree.Set([]byte("key3"), []byte("value3"))
	
	// Remove a key
	oldValue, err := tree.Remove([]byte("key2"))
	if err != nil {
		t.Errorf("Remove failed: %v", err)
	}
	if string(oldValue) != "value2" {
		t.Errorf("Expected 'value2', got '%s'", string(oldValue))
	}
	
	if tree.Size() != 2 {
		t.Errorf("Expected size 2, got %d", tree.Size())
	}
	
	// Verify key is removed
	value, _ := tree.Get([]byte("key2"))
	if value != nil {
		t.Errorf("Expected nil, got '%s'", string(value))
	}
	
	// Verify other keys still exist
	value, _ = tree.Get([]byte("key1"))
	if string(value) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", string(value))
	}
}

func TestSaveVersion(t *testing.T) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Add data
	tree.Set([]byte("key1"), []byte("value1"))
	tree.Set([]byte("key2"), []byte("value2"))
	
	// Save version
	version, err := tree.SaveVersion()
	if err != nil {
		t.Errorf("SaveVersion failed: %v", err)
	}
	if version != 1 {
		t.Errorf("Expected version 1, got %d", version)
	}
	
	// Add more data
	tree.Set([]byte("key3"), []byte("value3"))
	
	// Save another version
	version, err = tree.SaveVersion()
	if err != nil {
		t.Errorf("SaveVersion failed: %v", err)
	}
	if version != 2 {
		t.Errorf("Expected version 2, got %d", version)
	}
}

func TestIterator(t *testing.T) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Add data
	tree.Set([]byte("aaa"), []byte("value1"))
	tree.Set([]byte("bbb"), []byte("value2"))
	tree.Set([]byte("ccc"), []byte("value3"))
	
	// Test iterator
	itr, err := tree.Iterator(nil, nil)
	if err != nil {
		t.Errorf("Iterator failed: %v", err)
	}
	defer itr.Close()
	
	count := 0
	for itr.Valid() {
		count++
		t.Logf("Key: %s, Value: %s", string(itr.Key()), string(itr.Value()))
		itr.Next()
	}
	
	if count != 3 {
		t.Errorf("Expected 3 items, got %d", count)
	}
}

func TestHash(t *testing.T) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Empty tree hash
	emptyHash := tree.Hash()
	t.Logf("Empty tree hash: %x", emptyHash)
	
	// Add data
	tree.Set([]byte("key1"), []byte("value1"))
	
	// Non-empty tree hash
	hash1 := tree.Hash()
	t.Logf("Tree hash: %x", hash1)
	
	if len(hash1) != 32 {
		t.Errorf("Expected 32-byte hash, got %d bytes", len(hash1))
	}
}
