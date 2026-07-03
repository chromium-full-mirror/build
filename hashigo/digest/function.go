// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package digest

import (
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/twmb/murmur3"
	"github.com/zeebo/blake3"
	"google.golang.org/protobuf/proto"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// hasher describes how to compute digests for one REAPI digest function.
type hasher struct {
	fn          rpb.DigestFunction_Value // canonical enum value, never UNKNOWN.
	name        string                   // lowercase enum name (e.g. "sha256"), precomputed at init.
	newHash     func() hash.Hash         // constructor for the underlying hash.
	sum         func([]byte) string      // optional allocation-free one-shot hex digest; nil falls back to newHash.
	omitSegment bool                     // ByteStream resource names omit the {digest_function} segment.
	gitFraming  bool                     // prepend "blob <size>\0" before content (GITSHA1).
	hexLen      int                      // length of the hex-encoded hash, derived from newHash().Size().
	empty       Digest                   // digest of the empty blob, precomputed at init.
	emptyTree   Digest                   // digest of an empty Tree message, precomputed at init.
}

// One-shot digests for functions whose stdlib/library exposes one; these keep
// the hash state on the stack instead of heap-allocating a hash.Hash.
func sumSHA256(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func sumSHA1(b []byte) string   { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }
func sumMD5(b []byte) string    { s := md5.Sum(b); return hex.EncodeToString(s[:]) }
func sumSHA384(b []byte) string { s := sha512.Sum384(b); return hex.EncodeToString(s[:]) }
func sumSHA512(b []byte) string { s := sha512.Sum512(b); return hex.EncodeToString(s[:]) }
func sumBLAKE3(b []byte) string { s := blake3.Sum256(b); return hex.EncodeToString(s[:]) }

// registry maps each supported digest function to its hasher. Per the REAPI
// grammar the {digest_function} resource-name segment MUST be omitted for MD5,
// MURMUR3, SHA1, SHA256, SHA384, SHA512, and VSO (the server infers the
// function from the hash length and its advertised capabilities); GITSHA1,
// BLAKE3, and SHA256TREE each share a hash length with one of those and always
// carry an explicit segment. hexLen is derived here rather than in init() so
// that package-level vars depending on the registry (e.g. digestPattern) see
// it.
var registry = func() map[rpb.DigestFunction_Value]*hasher {
	r := map[rpb.DigestFunction_Value]*hasher{
		rpb.DigestFunction_SHA256:  {fn: rpb.DigestFunction_SHA256, newHash: sha256.New, sum: sumSHA256, omitSegment: true},
		rpb.DigestFunction_SHA1:    {fn: rpb.DigestFunction_SHA1, newHash: sha1.New, sum: sumSHA1, omitSegment: true},
		rpb.DigestFunction_GITSHA1: {fn: rpb.DigestFunction_GITSHA1, newHash: sha1.New, gitFraming: true},
		rpb.DigestFunction_BLAKE3:  {fn: rpb.DigestFunction_BLAKE3, newHash: func() hash.Hash { return blake3.New() }, sum: sumBLAKE3},
		rpb.DigestFunction_MD5:     {fn: rpb.DigestFunction_MD5, newHash: md5.New, sum: sumMD5, omitSegment: true},
		rpb.DigestFunction_SHA384:  {fn: rpb.DigestFunction_SHA384, newHash: sha512.New384, sum: sumSHA384, omitSegment: true},
		rpb.DigestFunction_SHA512:  {fn: rpb.DigestFunction_SHA512, newHash: sha512.New, sum: sumSHA512, omitSegment: true},
		// MurmurHash3 x64_128. The 16-byte digest is the library's canonical
		// serialization: the two 64-bit halves h1 then h2, each big-endian.
		rpb.DigestFunction_MURMUR3:    {fn: rpb.DigestFunction_MURMUR3, newHash: func() hash.Hash { return murmur3.New128() }, omitSegment: true},
		rpb.DigestFunction_VSO:        {fn: rpb.DigestFunction_VSO, newHash: func() hash.Hash { return newVSO() }, omitSegment: true},
		rpb.DigestFunction_SHA256TREE: {fn: rpb.DigestFunction_SHA256TREE, newHash: func() hash.Hash { return newSHA256Tree() }},
	}
	for _, h := range r {
		h.name = strings.ToLower(h.fn.String())
		h.hexLen = 2 * h.newHash().Size()
	}
	return r
}()

// omittedFunctions lists the digest functions whose resource-name segment is
// omitted, in enum-value order. InferOmitted resolves hash-length collisions
// in favor of the lowest enum value (MD5 over MURMUR3, both 32 hex).
var omittedFunctions = func() []rpb.DigestFunction_Value {
	var vs []rpb.DigestFunction_Value
	for v, h := range registry {
		if h.omitSegment {
			vs = append(vs, v)
		}
	}
	slices.Sort(vs)
	return vs
}()

func init() {
	emptyTreeBytes, err := proto.Marshal(&rpb.Tree{Root: &rpb.Directory{}})
	if err != nil {
		panic(err)
	}
	for _, h := range registry {
		h.empty = Digest{Hash: h.sumBytes(nil)}
		h.emptyTree = Digest{Hash: h.sumBytes(emptyTreeBytes), SizeBytes: int64(len(emptyTreeBytes))}
	}
}

// Function is a handle to one supported REAPI digest function. It is a small
// comparable value; callers hold one (obtained via Lookup, ParseFunction, or
// the SHA256 default) and compute all digests through it. The zero value is
// invalid and panics on use.
type Function struct {
	h *hasher
}

// SHA256 is the default REAPI digest function.
var SHA256 = Function{registry[rpb.DigestFunction_SHA256]}

// canonical maps the zero value (UNKNOWN) to SHA-256 per the REAPI inference
// rules, leaving everything else unchanged.
func canonical(fn rpb.DigestFunction_Value) rpb.DigestFunction_Value {
	if fn == rpb.DigestFunction_UNKNOWN {
		return rpb.DigestFunction_SHA256
	}
	return fn
}

// Lookup returns the Function for a digest-function enum value. UNKNOWN is
// treated as SHA-256 per the REAPI inference rules. It returns an error for
// recognized-but-unsupported values.
func Lookup(v rpb.DigestFunction_Value) (Function, error) {
	h, ok := registry[canonical(v)]
	if !ok {
		return Function{}, fmt.Errorf("unsupported digest function %q", v)
	}
	return Function{h}, nil
}

// ParseFunction converts a digest-function name (case-insensitive, e.g.
// "sha256", "sha1", "blake3") to its Function. It rejects names that are
// unknown or not supported.
func ParseFunction(name string) (Function, error) {
	v, ok := rpb.DigestFunction_Value_value[strings.ToUpper(name)]
	if !ok {
		return Function{}, fmt.Errorf("unknown digest function %q", name)
	}
	h, ok := registry[rpb.DigestFunction_Value(v)]
	if !ok {
		return Function{}, fmt.Errorf("unsupported digest function %q", name)
	}
	return Function{h}, nil
}

// FunctionByName resolves a ByteStream resource-name segment (the lowercase
// enum name, e.g. "sha1") to a Function. recognized reports whether name is a
// digest-function enum name at all, so resource-name parsers can tell "not a
// function segment, keep parsing" (false, nil) apart from "recognized but
// unsupported function" (true, error).
func FunctionByName(name string) (fn Function, recognized bool, err error) {
	v, ok := rpb.DigestFunction_Value_value[strings.ToUpper(name)]
	if !ok || rpb.DigestFunction_Value(v) == rpb.DigestFunction_UNKNOWN {
		return Function{}, false, nil
	}
	h, ok := registry[rpb.DigestFunction_Value(v)]
	if !ok {
		return Function{}, true, fmt.Errorf("unsupported digest function %q", name)
	}
	return Function{h}, true, nil
}

// SupportedFunctions returns the supported digest functions in enum-value
// order, so SHA-256 comes first.
func SupportedFunctions() []rpb.DigestFunction_Value {
	return slices.Sorted(maps.Keys(registry))
}

// Functions returns the supported digest functions as handles, in the same
// order as SupportedFunctions.
func Functions() []Function {
	vs := SupportedFunctions()
	fns := make([]Function, len(vs))
	for i, v := range vs {
		fns[i] = Function{registry[v]}
	}
	return fns
}

// InferOmitted maps a hex hash length to the digest function a client meant
// when it omitted the {digest_function} resource-name segment (see the
// registry comment for which functions omit it; explicit-segment functions
// like BLAKE3 are never candidates, so 64 hex resolves to SHA-256). The
// omittable lengths are distinct except for MD5 and MURMUR3 (both 32 hex);
// the inherent collision resolves in favor of MD5, so servers that advertise
// MURMUR3 must use InferOmittedFrom instead. Unmatched lengths fall back to
// SHA-256, leaving Validate to reject the invalid hash.
func InferOmitted(hexLen int) Function {
	for _, fn := range omittedFunctions {
		if registry[fn].hexLen == hexLen {
			return Function{registry[fn]}
		}
	}
	return SHA256
}

// InferOmittedFrom resolves an omitted {digest_function} resource-name segment
// against a server's advertised digest functions. ok is false when no
// advertised omitted-segment function has the given hex hash length.
// advertised must not contain two omitted-segment functions of the same hash
// length (MD5 and MURMUR3) — such a set is inherently ambiguous and a server
// must reject it at configuration time; with such a set the first match wins.
func InferOmittedFrom(advertised []Function, hexLen int) (fn Function, ok bool) {
	for _, f := range advertised {
		if f.OmitsSegment() && f.HexLen() == hexLen {
			return f, true
		}
	}
	return Function{}, false
}

// IsZero reports whether f is the invalid zero value.
func (f Function) IsZero() bool {
	return f.h == nil
}

// Value returns the digest-function enum value.
func (f Function) Value() rpb.DigestFunction_Value {
	return f.h.fn
}

// String returns the lowercase digest-function name (e.g. "sha256").
func (f Function) String() string {
	return f.h.name
}

// HexLen returns the length of the hex-encoded hash.
func (f Function) HexLen() int {
	return f.h.hexLen
}

// Matches reports whether v (with UNKNOWN meaning SHA-256 per the REAPI
// inference rules) is this digest function. It is used to decide whether
// persisted digests are still valid under the function in use.
func (f Function) Matches(v rpb.DigestFunction_Value) bool {
	return canonical(v) == f.h.fn
}

// OmitsSegment reports whether ByteStream resource names omit the
// {digest_function} path segment for this function (see the registry comment
// for the REAPI rule).
func (f Function) OmitsSegment() bool {
	return f.h.omitSegment
}

// ResourceNameSegment returns the ByteStream resource-name {digest_function}
// path segment (e.g. "gitsha1", "blake3"), or "" when the segment is omitted
// (see the registry comment for the REAPI rule).
func (f Function) ResourceNameSegment() string {
	if f.h.omitSegment {
		return ""
	}
	return f.String()
}

// Empty returns the digest of the empty blob.
func (f Function) Empty() Digest {
	return f.h.empty
}

// EmptyTree returns the digest of an empty tree (Tree message with empty root
// directory).
func (f Function) EmptyTree() Digest {
	return f.h.emptyTree
}

// IsHex reports whether c is a lowercase hexadecimal digit.
func IsHex(c byte) bool {
	return (c-'0' <= 9) || (c-'a' <= 5)
}

// Validate creates a Digest from a hash string and size, validating the hash
// length and characters against the function.
func (f Function) Validate(hash string, size int64) (Digest, error) {
	if size < 0 {
		return Digest{}, fmt.Errorf("expected non-negative size, got %d", size)
	}
	if len(hash) != f.h.hexLen {
		return Digest{}, fmt.Errorf("hash %q has invalid length %d, expected %d for %s", hash, len(hash), f.h.hexLen, f.h.fn)
	}
	for i := range len(hash) {
		if !IsHex(hash[i]) {
			return Digest{}, fmt.Errorf("hash %q contains invalid character %q at position %d", hash, hash[i], i)
		}
	}
	return Digest{
		Hash:      hash,
		SizeBytes: size,
	}, nil
}

// FromProto converts a rpb.Digest into a Digest, validating it against the
// function.
func (f Function) FromProto(d *rpb.Digest) (Digest, error) {
	return f.Validate(d.GetHash(), d.GetSizeBytes())
}

// gitHeader returns the git object header that GITSHA-1 prepends to the content.
func gitHeader(size int64) []byte {
	return []byte("blob " + strconv.FormatInt(size, 10) + "\x00")
}

// sumBytes computes the hex digest of in-memory content.
func (h *hasher) sumBytes(b []byte) string {
	if h.sum != nil {
		return h.sum(b)
	}
	hh := h.newHash()
	if h.gitFraming {
		hh.Write(gitHeader(int64(len(b))))
	}
	hh.Write(b)
	return hex.EncodeToString(hh.Sum(nil))
}

// NewContentHasher returns a hash.Hash for streaming content of the given
// size. For git-framing functions (GITSHA-1) it pre-writes the "blob <size>\0"
// header, so size must be the final content size; size is ignored for other
// functions.
func (f Function) NewContentHasher(size int64) hash.Hash {
	hh := f.h.newHash()
	if f.h.gitFraming {
		hh.Write(gitHeader(size))
	}
	return hh
}

// FromBytes computes the digest of in-memory content.
func (f Function) FromBytes(b []byte) Digest {
	return Digest{
		Hash:      f.h.sumBytes(b),
		SizeBytes: int64(len(b)),
	}
}

// FromReader computes the digest of content read from r. size is the content
// size; it is required for git-framing digest functions (GITSHA-1) and may be
// -1 (unknown) otherwise.
func (f Function) FromReader(r io.Reader, size int64) (Digest, error) {
	if f.h.gitFraming && size < 0 {
		return Digest{}, fmt.Errorf("digest function %s requires a known content size", f.h.fn)
	}
	hh := f.NewContentHasher(size)
	bufp := copyBufPool.Get().(*[]byte)
	n, err := io.CopyBuffer(hh, r, *bufp)
	copyBufPool.Put(bufp)
	if err != nil {
		return Digest{}, err
	}
	if f.h.gitFraming && n != size {
		return Digest{}, fmt.Errorf("content size mismatch: expected %d, read %d", size, n)
	}
	return Digest{
		Hash:      hex.EncodeToString(hh.Sum(nil)),
		SizeBytes: n,
	}, nil
}

// FromFile computes the digest of a file.
func (f Function) FromFile(path string) (Digest, error) {
	fh, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer fh.Close()

	size := int64(-1)
	if f.h.gitFraming {
		fi, err := fh.Stat()
		if err != nil {
			return Digest{}, err
		}
		size = fi.Size()
	}
	return f.FromReader(fh, size)
}

// FromMessage computes the digest of a proto message.
func (f Function) FromMessage(m proto.Message) (Digest, error) {
	mb, err := proto.Marshal(m)
	if err != nil {
		return Digest{}, err
	}
	return f.FromBytes(mb), nil
}
