// Package digest contains functions to simplify handling content digests.
package digest

import (
	"crypto"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/proto"
)

var (
	// hashFn is the digest function used.
	hashFn = crypto.SHA256

	// hashHexLen is the length of the hex-encoded hash.
	hashHexLen = hex.EncodedLen(hashFn.Size())

	// Empty is the digest of the empty blob.
	Empty = FromBlob([]byte{})

	// copyBufs is a pool of 32KiB []byte slices, used to compute hashes.
	copyBufs = sync.Pool{
		New: func() any {
			buf := make([]byte, 32*1024)
			return &buf
		},
	}
)

// Digest is a Go type to mirror the repb.Digest message.
type Digest struct {
	Hash string
	Size int64
}

// ToProto converts a Digest into a repb.Digest. No validation is performed!
func (d Digest) ToProto() *repb.Digest {
	return &repb.Digest{
		Hash:      d.Hash,
		SizeBytes: d.Size,
	}
}

// String returns a hash in a canonical form of hash/size.
func (d Digest) String() string {
	if d.Hash == "" && d.Size == 0 {
		return ""
	}
	return fmt.Sprintf("%s/%d", d.Hash, d.Size)
}

// IsHex reports whether c is a lowercase hexadecimal digit.
func IsHex(c byte) bool {
	return (c-'0' <= 9) || (c-'a' <= 5)
}

// New creates a new digest from a string and size. It does some basic
// validation, which makes it marginally superior to constructing a Digest
// yourself. It returns an empty digest and an error if the hash/size are invalid.
func New(hash string, size int64) (Digest, error) {
	if size < 0 {
		return Empty, fmt.Errorf("expected non-negative size, got %d", size)
	}
	if len(hash) != hashHexLen {
		return Empty, fmt.Errorf("hash %q has invalid length %d, expected %d", hash, len(hash), hashHexLen)
	}
	for i := range len(hash) {
		if !IsHex(hash[i]) {
			return Empty, fmt.Errorf("hash %q contains invalid character %q at position %d", hash, hash[i], i)
		}
	}
	return Digest{
		Hash: hash,
		Size: size,
	}, nil
}

// NewFromProto converts a repb.Digest into a Digest.
func NewFromProto(d *repb.Digest) (Digest, error) {
	return New(d.Hash, d.SizeBytes)
}

// FromBlob computes the digest of a blob.
func FromBlob(blob []byte) Digest {
	h := hashFn.New()
	h.Write(blob)
	return Digest{
		Hash: hex.EncodeToString(h.Sum(nil)),
		Size: int64(len(blob)),
	}
}

// FromFile computes the digest of a file.
func FromFile(path string) (Digest, error) {
	h := hashFn.New()

	f, err := os.Open(path)
	if err != nil {
		return Empty, err
	}
	defer f.Close()

	buf := copyBufs.Get().(*[]byte)
	defer copyBufs.Put(buf)

	size, err := io.CopyBuffer(h, f, *buf)
	if err != nil {
		return Empty, err
	}

	return Digest{
		Hash: hex.EncodeToString(h.Sum(nil)),
		Size: size,
	}, nil
}

// FromMessage computes the digest of a proto message.
func FromMessage(m proto.Message) (Digest, error) {
	mb, err := proto.Marshal(m)
	if err != nil {
		return Empty, err
	}
	return FromBlob(mb), nil
}
