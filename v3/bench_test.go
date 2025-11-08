package iavl

import (
	"fmt"
	"testing"
	
	"github.com/cosmos/iavl/db"
)

func BenchmarkSet(b *testing.B) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		value := []byte(fmt.Sprintf("value-%d", i))
		tree.Set(key, value)
	}
}

func BenchmarkGet(b *testing.B) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Setup
	for i := 0; i < 10000; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		value := []byte(fmt.Sprintf("value-%d", i))
		tree.Set(key, value)
	}
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		key := []byte(fmt.Sprintf("key-%d", i%10000))
		tree.Get(key)
	}
}

func BenchmarkSaveVersion(b *testing.B) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Setup
	for i := 0; i < 1000; i++ {
		key := []byte(fmt.Sprintf("key-%d", i))
		value := []byte(fmt.Sprintf("value-%d", i))
		tree.Set(key, value)
	}
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		tree.SaveVersion()
	}
}

func BenchmarkIterator(b *testing.B) {
	memdb := db.NewMemDB()
	tree := NewMutableTree(memdb, 0, NewNopLogger())
	
	// Setup
	for i := 0; i < 10000; i++ {
		key := []byte(fmt.Sprintf("key-%08d", i))
		value := []byte(fmt.Sprintf("value-%d", i))
		tree.Set(key, value)
	}
	
	b.ResetTimer()
	
	for i := 0; i < b.N; i++ {
		itr, _ := tree.Iterator(nil, nil)
		count := 0
		for itr.Valid() {
			count++
			itr.Next()
		}
		itr.Close()
		if count != 10000 {
			b.Errorf("Expected 10000 items, got %d", count)
		}
	}
}
