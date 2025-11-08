package iavl

import (
	corestore "cosmossdk.io/core/store"
)

// Options for configuring the IAVL tree
type Options struct {
	// Database for persistence
	DB corestore.KVStoreWithBatch

	// Cache size for nodes (0 for no cache)
	CacheSize int

	// Initial version (default: 1)
	InitialVersion int64

	// Logger
	Logger Logger
}

// DefaultOptions returns default configuration
func DefaultOptions() Options {
	return Options{
		CacheSize:      100000,
		InitialVersion: 1,
		Logger:         &nopLogger{},
	}
}

// Option is a functional option for configuring the tree
type Option func(*Options)

// WithDB sets the database
func WithDB(db corestore.KVStoreWithBatch) Option {
	return func(o *Options) {
		o.DB = db
	}
}

// WithCacheSize sets the cache size
func WithCacheSize(size int) Option {
	return func(o *Options) {
		o.CacheSize = size
	}
}

// WithInitialVersion sets the initial version
func WithInitialVersion(v int64) Option {
	return func(o *Options) {
		o.InitialVersion = v
	}
}

// WithLogger sets the logger
func WithLogger(logger Logger) Option {
	return func(o *Options) {
		o.Logger = logger
	}
}
