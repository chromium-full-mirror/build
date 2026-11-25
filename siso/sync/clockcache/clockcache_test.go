// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package clockcache

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sync"
	"sync/atomic"
	"testing"
)

// val is a cache value whose estimated size is its size field.
type val struct {
	name string
	size int64
}

func valSize(_ string, v *val) int64 { return v.size }

func newCache(maxBytes int64) *Cache[string, *val] {
	c := &Cache[string, *val]{}
	c.Init(maxBytes, valSize)
	return c
}

func TestResolveBytes(t *testing.T) {
	const mib = int64(1 << 20)
	for _, tc := range []struct {
		name     string
		optBytes int64
		memLimit int64
		want     int64
	}{
		{name: "no_limit", memLimit: math.MaxInt64, want: 0},
		{name: "zero_limit", memLimit: 0, want: 0},
		{name: "6418MiB", memLimit: 6418 * mib, want: 6418 * mib / 20},
		{name: "tiny_limit_floors_at_1MiB", memLimit: 4 * mib, want: mib},
		{name: "explicit_cap", optBytes: 128 * mib, memLimit: 6418 * mib, want: 128 * mib},
		{name: "explicit_unbounded", optBytes: -1, memLimit: 6418 * mib, want: 0},
	} {
		if got := ResolveBytes(tc.optBytes, tc.memLimit, 20); got != tc.want {
			t.Errorf("%s: ResolveBytes(%d, %d, 20)=%d; want %d", tc.name, tc.optBytes, tc.memLimit, got, tc.want)
		}
	}
}

// TestZeroSize checks that entries whose size is estimated as 0 still
// count toward the cap.
func TestZeroSize(t *testing.T) {
	var c Cache[int, int]
	c.Init(1000, func(int, int) int64 { return 0 })
	for i := range 2000 {
		c.LoadOrStore(i, i)
	}
	if st := c.Stats(); st.Entries > 1000 || st.Evictions == 0 {
		t.Errorf("Stats()=%+v; want Entries <= 1000, Evictions > 0", st)
	}
}

// TestUnboundedNilInterface checks that an unbounded cache of interface
// values can hold nil, like a bounded one.
func TestUnboundedNilInterface(t *testing.T) {
	var c Cache[string, any]
	if got := c.LoadOrStore("k", nil); got != nil {
		t.Errorf("LoadOrStore(k, nil)=%v; want nil", got)
	}
	if got, ok := c.Load("k"); !ok || got != nil {
		t.Errorf("Load(k)=%v, %t; want nil, true", got, ok)
	}
}

func TestInit(t *testing.T) {
	for _, maxBytes := range []int64{0, -1, 4096, 1 << 30} {
		c := newCache(maxBytes)
		want := max(maxBytes, 0)
		if got := c.Stats().CapBytes; got != want {
			t.Errorf("Init(%d): CapBytes=%d; want %d", maxBytes, got, want)
		}
	}
}

func TestCLOCK(t *testing.T) {
	a := &val{name: "a", size: 100}
	b := &val{name: "b", size: 100}
	c := &val{name: "c", size: 100}
	// A cap that holds 2 entries.
	cache := newCache(200)

	if got := cache.LoadOrStore("a", a); got != a {
		t.Fatalf("LoadOrStore(a)=%v; want %v", got, a)
	}
	if got := cache.LoadOrStore("b", b); got != b {
		t.Fatalf("LoadOrStore(b)=%v; want %v", got, b)
	}
	// a is used, b is not.
	if got, ok := cache.Load("a"); !ok || got != a {
		t.Fatalf("Load(a)=%v, %t; want %v, true", got, ok, a)
	}
	// Storing c evicts b and keeps a.
	if got := cache.LoadOrStore("c", c); got != c {
		t.Fatalf("LoadOrStore(c)=%v; want %v", got, c)
	}
	if _, ok := cache.Load("b"); ok {
		t.Errorf("Load(b)=_, true after eviction; want false")
	}
	for _, v := range []*val{a, c} {
		if got, ok := cache.Load(v.name); !ok || got != v {
			t.Errorf("Load(%s)=%v, %t; want %v, true", v.name, got, ok, v)
		}
	}
	st := cache.Stats()
	if st.Evictions != 1 || st.Entries != 2 || st.Bytes != 200 {
		t.Errorf("Stats()=%+v; want 1 eviction, 2 entries, 200 bytes", st)
	}

	// A value larger than the cap is returned without being stored,
	// and doesn't evict anything.
	big := &val{name: "big", size: 201}
	if got := cache.LoadOrStore("big", big); got != big {
		t.Errorf("LoadOrStore(big)=%v; want %v", got, big)
	}
	if _, ok := cache.Load("big"); ok {
		t.Errorf("Load(big)=_, true; want false")
	}
	if st := cache.Stats(); st.Oversized != 1 || st.Evictions != 1 || st.Entries != 2 {
		t.Errorf("Stats()=%+v; want 1 oversized, 1 eviction, 2 entries", st)
	}
	checkInvariants(t, cache)
}

// TestCLOCKAllUsed checks that eviction makes progress when every entry
// is used, and that it evicts several entries for a large value.
func TestCLOCKAllUsed(t *testing.T) {
	cache := newCache(400)
	for i := range 4 {
		cache.LoadOrStore(fmt.Sprint(i), &val{name: fmt.Sprint(i), size: 100})
	}
	for i := range 4 {
		cache.Load(fmt.Sprint(i))
	}
	cache.LoadOrStore("x", &val{name: "x", size: 300})
	st := cache.Stats()
	if st.Evictions != 3 || st.Entries != 2 || st.Bytes != 400 {
		t.Errorf("Stats()=%+v; want 3 evictions, 2 entries, 400 bytes", st)
	}
	if _, ok := cache.Load("x"); !ok {
		t.Errorf("Load(x)=_, false; want true")
	}
	checkInvariants(t, cache)
}

func TestUnbounded(t *testing.T) {
	var cache Cache[string, *val]
	a := &val{name: "a", size: 1 << 40}
	if got := cache.LoadOrStore("a", a); got != a {
		t.Fatalf("LoadOrStore(a)=%v; want %v", got, a)
	}
	if got := cache.LoadOrStore("a", &val{name: "a2"}); got != a {
		t.Errorf("second LoadOrStore(a)=%v; want %v", got, a)
	}
	if got, ok := cache.Load("a"); !ok || got != a {
		t.Errorf("Load(a)=%v, %t; want %v, true", got, ok, a)
	}
	if st := cache.Stats(); st != (Stats{}) {
		t.Errorf("Stats()=%+v; want zero", st)
	}
}

func checkInvariants(t *testing.T, c *Cache[string, *val]) {
	t.Helper()
	if c.capBytes == 0 {
		return
	}
	c.mu.Lock()
	var n int
	var sum int64
	if c.hand != nil {
		seen := make(map[*entry[string, *val]]bool)
		for e := c.hand; ; {
			if seen[e] {
				t.Errorf("cycle before the hand at step %d", n)
				break
			}
			seen[e] = true
			if e.next == nil || e.prev == nil {
				t.Errorf("nil link at step %d", n)
				break
			}
			if e.next.prev != e || e.prev.next != e {
				t.Errorf("broken links at step %d", n)
			}
			if v, ok := c.entries.Load(e.key); !ok || v != e {
				t.Errorf("entries[%q]=%v, %t; want %p, true", e.key, v, ok, e)
			}
			n++
			sum += e.bytes
			e = e.next
			if e == c.hand {
				break
			}
		}
	}
	if n != c.count {
		t.Errorf("ring has %d entries; want count=%d", n, c.count)
	}
	if sum != c.bytes {
		t.Errorf("entries have %d bytes; want bytes=%d", sum, c.bytes)
	}
	if c.bytes > c.capBytes {
		t.Errorf("bytes=%d; want <= %d", c.bytes, c.capBytes)
	}
	c.mu.Unlock()
	var nmap int
	c.entries.Range(func(k, v any) bool {
		nmap++
		return true
	})
	if nmap != n {
		t.Errorf("entries has %d keys; want %d entries in the ring", nmap, n)
	}
}

func TestConcurrentSameKey(t *testing.T) {
	for _, capBytes := range []int64{0, 4 << 20} {
		t.Run(fmt.Sprintf("cap_%d", capBytes), func(t *testing.T) {
			cache := newCache(capBytes)
			const workers = 32
			for round := range 16 {
				key := fmt.Sprintf("same-key-%d", round)
				results := make([]*val, workers)
				start := make(chan struct{})
				var wg sync.WaitGroup
				for w := range workers {
					wg.Go(func() {
						v := &val{name: key, size: 100}
						<-start
						results[w] = cache.LoadOrStore(key, v)
					})
				}
				close(start)
				wg.Wait()
				for w, got := range results {
					if got != results[0] {
						t.Errorf("round %d worker %d: got %p; want %p", round, w, got, results[0])
					}
				}
				if got, ok := cache.Load(key); !ok || got != results[0] {
					t.Errorf("round %d: Load=%p, %t; want %p, true", round, got, ok, results[0])
				}
			}
			checkInvariants(t, cache)
		})
	}
}

func TestRandomizedInvariants(t *testing.T) {
	const (
		capBytes = 4096
		baseSize = 400
		bigSize  = 5000
		nKeys    = 512
	)
	cache := newCache(capBytes)
	rng := rand.New(rand.NewPCG(42, 99))
	vals := make([]*val, nKeys)
	for i := range vals {
		size := baseSize + rng.Int64N(baseSize)
		if i%37 == 0 {
			size = bigSize
		}
		vals[i] = &val{name: fmt.Sprintf("key-%d", i), size: size}
	}
	for step := range 5000 {
		i := rng.IntN(nKeys)
		if rng.IntN(4) == 0 {
			// a quarter of the accesses go to hot keys.
			i = rng.IntN(32)
		}
		if rng.IntN(2) == 0 {
			cache.Load(vals[i].name)
		} else if got := cache.LoadOrStore(vals[i].name, vals[i]); got != vals[i] {
			t.Fatalf("step %d: LoadOrStore=%v; want %v", step, got, vals[i])
		}
		if (step+1)%250 == 0 {
			checkInvariants(t, cache)
		}
	}
	st := cache.Stats()
	if st.Evictions == 0 || st.Oversized == 0 {
		t.Errorf("Stats()=%+v; want evictions and oversized", st)
	}
}

// TestConcurrentHotReadEviction checks that the cap holds and that each
// insert evicts one entry while Loads keep marking entries used. (It can't
// show a stall without the bound on skips in evictLocked.)
func TestConcurrentHotReadEviction(t *testing.T) {
	const inserts = 500
	const size = 100
	for _, n := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d_entries", n), func(t *testing.T) {
			cache := newCache(size * int64(n))
			keys := make([]string, inserts)
			for i := range keys {
				keys[i] = fmt.Sprintf("hot-%04d", i)
			}
			var head atomic.Int32
			var done atomic.Bool
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					for !done.Load() {
						hi := int(head.Load())
						for j := max(hi-n, 0); j <= hi; j++ {
							cache.Load(keys[j])
						}
					}
				})
			}
			for i, k := range keys {
				cache.LoadOrStore(k, &val{name: k, size: size})
				head.Store(int32(i))
				if b := cache.Stats().Bytes; b > size*int64(n) {
					t.Errorf("insert %d: %d bytes; want <= %d", i, b, size*n)
				}
			}
			done.Store(true)
			wg.Wait()
			if ev, want := cache.Stats().Evictions, int64(inserts-n); ev != want {
				t.Errorf("Evictions=%d; want %d", ev, want)
			}
			checkInvariants(t, cache)
		})
	}
}

func BenchmarkLoadParallel(b *testing.B) {
	const nKeys = 1024
	keys := make([]string, nKeys)
	for i := range keys {
		keys[i] = fmt.Sprintf("bench-%d", i)
	}
	for _, mode := range []struct {
		name     string
		capBytes int64
	}{
		{name: "sync_map", capBytes: 0},
		{name: "bounded_clock", capBytes: 64 << 20},
	} {
		b.Run(mode.name, func(b *testing.B) {
			cache := newCache(mode.capBytes)
			v := &val{name: "v", size: 100}
			for _, k := range keys {
				cache.LoadOrStore(k, v)
			}
			b.Run("single_key", func(b *testing.B) {
				b.RunParallel(func(pb *testing.PB) {
					for pb.Next() {
						if _, ok := cache.Load(keys[0]); !ok {
							b.Error("unexpected cache miss")
							return
						}
					}
				})
			})
			b.Run("spread_keys", func(b *testing.B) {
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						if _, ok := cache.Load(keys[i%nKeys]); !ok {
							b.Error("unexpected cache miss")
							return
						}
						i++
					}
				})
			})
		})
	}
}

// TestFilled checks that a full cache holds its cap within one value.
func TestFilled(t *testing.T) {
	const valBytes = 1024
	var c Cache[int, int]
	c.Init(minBytes, func(int, int) int64 { return valBytes })
	const n = 100000
	for i := range n {
		c.LoadOrStore(i, i)
	}
	st := c.Stats()
	if st.Bytes > st.CapBytes || st.Bytes < st.CapBytes-valBytes {
		t.Errorf("Bytes=%d; want within %d of cap %d", st.Bytes, valBytes, st.CapBytes)
	}
	if want := int64(n - st.Entries); st.Evictions != want {
		t.Errorf("Evictions=%d; want %d (inserts - Entries)", st.Evictions, want)
	}
}
