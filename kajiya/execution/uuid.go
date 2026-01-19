package execution

import (
	"bufio"
	"crypto/rand"
	"sync"

	"github.com/google/uuid"
)

var uuidgen = NewUUIDGenerator()

type UUIDGenerator struct {
	pool *sync.Pool
}

func NewUUIDGenerator() *UUIDGenerator {
	return &UUIDGenerator{
		pool: &sync.Pool{
			New: func() any {
				return bufio.NewReader(rand.Reader)
			},
		},
	}
}

func (g *UUIDGenerator) NewV4() uuid.UUID {
	reader := g.pool.Get().(*bufio.Reader)
	defer g.pool.Put(reader)
	return uuid.Must(uuid.NewRandomFromReader(reader))
}

func (g *UUIDGenerator) NewV7() uuid.UUID {
	reader := g.pool.Get().(*bufio.Reader)
	defer g.pool.Put(reader)
	return uuid.Must(uuid.NewV7FromReader(reader))
}
