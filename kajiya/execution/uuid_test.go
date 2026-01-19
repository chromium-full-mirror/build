package execution

import (
	"bufio"
	"crypto/rand"
	"sync"
	"testing"

	"github.com/google/uuid"
)

// Benchmark of our current implementation.

func BenchmarkUUIDGenerator(b *testing.B) {
	uuidgen := NewUUIDGenerator()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			u := uuidgen.NewV4()
			if u == uuid.Nil {
				b.Fatal("expected non-nil UUID")
			}
		}
	})
}

// Benchmarks of various random UUID generation methods.
//
// goos: linux, goarch: amd64
// cpu: AMD Ryzen 9 9950X3D 16-Core Processor
//
//                           | 1 CPU | 2 CPUs | 4 CPUs | 8 CPUs | 16 CPUs
//                  NoReader | 68.54 |  37.13 |  20.77 |  15.31 |  17.95 ns/op
//            RandPoolReader | 48.33 |  68.07 |  83.93 |  84.53 |  86.73 ns/op
// ThreadLocalBufferedReader | 44.56 |  23.33 |  12.51 |   6.95 |   5.40 ns/op
//       MutexBufferedReader | 50.07 |  60.83 |  62.31 |  63.40 |  64.66 ns/op
//            SyncPoolReader | 49.08 |  26.17 |  13.52 |   7.66 |   5.98 ns/op

func BenchmarkNoReader(b *testing.B) {
	uuid.DisableRandPool()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			u := uuid.Must(uuid.NewRandom())
			if u == uuid.Nil {
				b.Fatal("expected non-nil UUID")
			}
		}
	})
}

func BenchmarkRandPoolReader(b *testing.B) {
	uuid.EnableRandPool()
	defer uuid.DisableRandPool()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			u := uuid.Must(uuid.NewRandom())
			if u == uuid.Nil {
				b.Fatal("expected non-nil UUID")
			}
		}
	})
}

func BenchmarkThreadLocalBufferedReader(b *testing.B) {
	b.RunParallel(func(pb *testing.PB) {
		r := bufio.NewReader(rand.Reader)
		for pb.Next() {
			u := uuid.Must(uuid.NewRandomFromReader(r))
			if u == uuid.Nil {
				b.Fatal("expected non-nil UUID")
			}
		}
	})
}

func BenchmarkMutexBufferedReader(b *testing.B) {
	var mu sync.Mutex
	r := bufio.NewReader(rand.Reader)
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			mu.Lock()
			u := uuid.Must(uuid.NewRandomFromReader(r))
			mu.Unlock()
			if u == uuid.Nil {
				b.Fatal("expected non-nil UUID")
			}
		}
	})
}

func BenchmarkSyncPoolReader(b *testing.B) {
	var readerPool = sync.Pool{
		New: func() any {
			return bufio.NewReader(rand.Reader)
		},
	}
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			r := readerPool.Get().(*bufio.Reader)
			u := uuid.Must(uuid.NewRandomFromReader(r))
			if u == uuid.Nil {
				b.Fatal("expected non-nil UUID")
			}
			readerPool.Put(r)
		}
	})
}
