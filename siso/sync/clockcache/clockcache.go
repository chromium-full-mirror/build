// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package clockcache provides a concurrent cache that can be bounded by the
// estimated bytes of its entries, with CLOCK eviction.
package clockcache

import (
	"math"
	"sync"
	"sync/atomic"
)

// minBytes is the smallest cap that ResolveBytes returns.
const minBytes = 1 << 20

// ResolveBytes returns the cap for a cache from a flag value optBytes:
// optBytes if it is positive, 0 (unbounded) if it is negative, and for 0,
// 1/divisor of the Go memory limit memLimit, but at least 1 MiB, or 0 if
// there is no memory limit.
func ResolveBytes(optBytes, memLimit, divisor int64) int64 {
	switch {
	case optBytes > 0:
		return optBytes
	case optBytes < 0 || divisor <= 0 || memLimit <= 0 || memLimit == math.MaxInt64:
		return 0
	}
	return max(memLimit/divisor, minBytes)
}

// Cache maps keys to values. Values are shared and must not be modified.
// The zero value is unbounded; Init bounds it with CLOCK eviction.
type Cache[K comparable, V any] struct {
	// entries maps K to V if unbounded, or to *entry[K, V] if bounded.
	// In bounded mode, mu serializes Stores and Deletes, while Loads
	// are lock-free.
	entries  sync.Map
	capBytes int64
	size     func(K, V) int64

	mu        sync.Mutex   // guards the fields below and Stores/Deletes of entries
	hand      *entry[K, V] // CLOCK hand in a circular doubly-linked ring
	count     int
	bytes     int64
	evictions int64
	oversized atomic.Int64
}

type entry[K comparable, V any] struct {
	key   K
	val   V
	bytes int64
	used  atomic.Bool
	prev  *entry[K, V]
	next  *entry[K, V]
}

// touch marks the entry as recently used.
func (e *entry[K, V]) touch() {
	if !e.used.Load() {
		e.used.Store(true)
	}
}

// Init bounds the cache to maxBytes, as estimated by size for each entry.
// It must be called before the cache is used. If maxBytes <= 0, the cache
// stays unbounded.
func (c *Cache[K, V]) Init(maxBytes int64, size func(K, V) int64) {
	if maxBytes <= 0 {
		return
	}
	if size == nil {
		panic("clockcache: Init with nil size")
	}
	c.capBytes = maxBytes
	c.size = size
}

// Load returns the value for k, if present.
func (c *Cache[K, V]) Load(k K) (V, bool) {
	v, ok := c.entries.Load(k)
	if !ok {
		var zero V
		return zero, false
	}
	if c.capBytes == 0 {
		val, _ := v.(V) // v may be a nil interface value.
		return val, true
	}
	e := v.(*entry[K, V])
	e.touch()
	return e.val, true
}

// LoadOrStore returns the value for k if present. Otherwise it stores and
// returns v, evicting cold entries if needed. In a bounded cache, a value
// larger than the cap is returned without being stored.
func (c *Cache[K, V]) LoadOrStore(k K, v V) V {
	if c.capBytes == 0 {
		got, _ := c.entries.LoadOrStore(k, v)
		val, _ := got.(V) // got may be a nil interface value.
		return val
	}
	if got, ok := c.Load(k); ok {
		return got
	}
	// at least 1, so that each entry counts toward the cap.
	sz := max(c.size(k, v), 1)
	if sz > c.capBytes {
		c.oversized.Add(1)
		return v
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if got, ok := c.Load(k); ok {
		return got
	}
	c.evictLocked(c.capBytes - sz)
	c.insertLocked(&entry[K, V]{key: k, val: v, bytes: sz})
	return v
}

// Stats are counters of a bounded cache.
type Stats struct {
	// CapBytes is the byte cap, or 0 if the cache is unbounded.
	CapBytes int64
	// Bytes and Entries are the estimated bytes and number of the
	// entries in the cache.
	Bytes   int64
	Entries int
	// Evictions is the number of evicted entries.
	Evictions int64
	// Oversized is the number of values not stored because they were
	// larger than the cap.
	Oversized int64
}

// Stats returns the counters of the cache. They are all 0 if the cache is
// unbounded.
func (c *Cache[K, V]) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Stats{
		CapBytes:  c.capBytes,
		Bytes:     c.bytes,
		Entries:   c.count,
		Evictions: c.evictions,
		Oversized: c.oversized.Load(),
	}
}

// evictLocked evicts entries until c.bytes <= targetBytes. It skips a used
// entry at most once, so concurrent Loads can't stall it.
func (c *Cache[K, V]) evictLocked(targetBytes int64) {
	skips := 0
	for c.bytes > targetBytes && c.hand != nil {
		e := c.hand
		if skips < c.count && e.used.Swap(false) {
			skips++
			c.hand = e.next
			continue
		}
		c.entries.Delete(e.key)
		c.bytes -= e.bytes
		c.count--
		c.evictions++
		if e.next == e {
			c.hand = nil
		} else {
			e.prev.next = e.next
			e.next.prev = e.prev
			c.hand = e.next
		}
		e.prev = nil
		e.next = nil
	}
}

// insertLocked inserts e behind c.hand, so that older entries are
// considered for eviction first.
func (c *Cache[K, V]) insertLocked(e *entry[K, V]) {
	c.bytes += e.bytes
	c.count++
	if c.hand == nil {
		e.prev = e
		e.next = e
		c.hand = e
	} else {
		prev := c.hand.prev
		e.prev = prev
		e.next = c.hand
		prev.next = e
		c.hand.prev = e
	}
	c.entries.Store(e.key, e)
}
