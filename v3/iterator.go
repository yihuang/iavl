package iavl

import (
	corestore "cosmossdk.io/core/store"
)

// Iterator iterates over the tree's latest state
type Iterator struct {
	db     *Database
	itr    corestore.Iterator
	start  []byte
	end    []byte
	valid  bool
	err    error
}

// NewIterator creates a new iterator
func NewIterator(db *Database, start, end []byte) (*Iterator, error) {
	itr, err := db.LatestStateIterator()
	if err != nil {
		return nil, err
	}
	
	it := &Iterator{
		db:    db,
		itr:   itr,
		start: start,
		end:   end,
	}
	
	// Seek to start
	it.Seek(start)
	
	return it, nil
}

// Seek moves the iterator to the first key greater than or equal to the given key
func (it *Iterator) Seek(key []byte) {
	if it.itr == nil {
		it.valid = false
		return
	}
	
	// If key is nil, we start from the beginning
	// The iterator is already positioned at the start
	it.valid = it.itr.Valid()
	
	if it.valid {
		// Check if key is within range
		k := it.Key()
		if it.start != nil && compareKeys(k, it.start) < 0 {
			it.Next()
		}
		if it.end != nil && compareKeys(k, it.end) >= 0 {
			it.valid = false
		}
	}
}

// Next moves to the next key
func (it *Iterator) Next() {
	if it.itr == nil || !it.valid {
		return
	}
	
	it.itr.Next()
	it.valid = it.itr.Valid()
	
	if it.valid {
		// Check if key is within range
		k := it.Key()
		if it.end != nil && compareKeys(k, it.end) >= 0 {
			it.valid = false
		}
	}
}

// Valid returns whether the iterator is valid
func (it *Iterator) Valid() bool {
	return it.valid && it.itr != nil && it.itr.Valid()
}

// Key returns the current key
func (it *Iterator) Key() []byte {
	if !it.valid {
		return nil
	}
	
	key := it.itr.Key()
	// Strip the prefix
	if len(key) > 0 && key[0] == latestStatePrefix {
		return key[1:]
	}
	return key
}

// Value returns the current value
func (it *Iterator) Value() []byte {
	if !it.valid {
		return nil
	}
	return it.itr.Value()
}

// Error returns the last error
func (it *Iterator) Error() error {
	if it.err != nil {
		return it.err
	}
	if it.itr != nil {
		return it.itr.Error()
	}
	return nil
}

// Close closes the iterator
func (it *Iterator) Close() error {
	if it.itr != nil {
		return it.itr.Close()
	}
	return nil
}

// compareKeys compares two keys
func compareKeys(a, b []byte) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}
	
	minLen := len(a)
	if len(b) < minLen {
		minLen = len(b)
	}
	
	for i := 0; i < minLen; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}
