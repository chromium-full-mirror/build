package digest

import (
	"encoding/hex"
	"hash"
	"io/fs"
	"os"
	"sync"
)

type HashingFileWriter struct {
	file   *os.File
	path   string
	hasher hash.Hash

	mu   sync.Mutex
	size int64
}

// NewHashingFileWriter creates a new file at the given path and returns a HashingFileWriter.
func NewHashingFileWriter(path string) (*HashingFileWriter, error) {
	flags := os.O_WRONLY
	if path != os.DevNull {
		flags |= os.O_CREATE | os.O_EXCL
	}
	f, err := os.OpenFile(path, flags, 0644)
	if err != nil {
		return nil, err
	}
	return &HashingFileWriter{
		file:   f,
		path:   path,
		hasher: hashFn.New(),
	}, nil
}

// Write writes the given data to the file and updates the hash and size.
func (h *HashingFileWriter) Write(p []byte) (n int, err error) {
	if h == nil || h.file == nil {
		return 0, fs.ErrInvalid
	}
	n, err = h.file.Write(p)
	if n > 0 {
		h.hasher.Write(p[:n])

		h.mu.Lock()
		h.size += int64(n)
		h.mu.Unlock()
	}
	return n, err
}

// Close closes the file.
func (h *HashingFileWriter) Close() error {
	if h == nil || h.file == nil {
		return fs.ErrInvalid
	}
	err := h.file.Close()
	h.file = nil
	return err
}

// Delete closes and then deletes the file.
func (h *HashingFileWriter) Delete() error {
	if h == nil || h.path == "" {
		return fs.ErrInvalid
	}
	_ = h.Close()
	err := os.Remove(h.path)
	h.path = ""
	return err
}

// Path returns the path to the file.
func (h *HashingFileWriter) Path() string {
	if h == nil {
		return ""
	}
	return h.path
}

// Hash returns the hex-encoded hash of the data that has been written so far.
// It is safe to call this at any time, even after Close() or Delete().
func (h *HashingFileWriter) Hash() string {
	if h == nil {
		return Empty.Hash
	}
	return hex.EncodeToString(h.hasher.Sum(nil))
}

// Size returns the number of bytes that have been written so far.
// It is safe to call this at any time, even after Close() or Delete().
func (h *HashingFileWriter) Size() int64 {
	if h == nil {
		return 0
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.size
}

// Digest returns the digest of the data that has been written so far.
// It is safe to call this at any time, even after Close() or Delete().
func (h *HashingFileWriter) Digest() Digest {
	return Digest{
		Hash: h.Hash(),
		Size: h.Size(),
	}
}
