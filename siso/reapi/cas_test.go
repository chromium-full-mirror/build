// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package reapi

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/klauspost/compress/zstd"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/auth/cred"
	"go.chromium.org/build/siso/blob"
)

// errReader returns err on every Read.
type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestExpectEOF(t *testing.T) {
	sentinel := errors.New("read failed")
	for _, tc := range []struct {
		name     string
		r        io.Reader
		wantEOF  bool  // expectEOF should report a clean EOF (nil)
		wantErrs error // if non-nil, expectEOF's error must match via errors.Is
	}{
		{
			name:    "clean_eof",
			r:       bytes.NewReader(nil),
			wantEOF: true,
		},
		{
			name: "trailing_data",
			r:    bytes.NewReader([]byte("x")),
		},
		{
			name:     "read_error",
			r:        errReader{err: sentinel},
			wantErrs: sentinel,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := expectEOF(tc.r)
			switch {
			case tc.wantEOF:
				if err != nil {
					t.Errorf("expectEOF=%v; want nil", err)
				}
			case tc.wantErrs != nil:
				if !errors.Is(err, tc.wantErrs) {
					t.Errorf("expectEOF=%v; want %v", err, tc.wantErrs)
				}
			default:
				if err == nil {
					t.Errorf("expectEOF=nil; want trailing-data error")
				}
			}
		})
	}
}

func TestCreateBatchUpdateBlobsRequests(t *testing.T) {
	ctx := t.Context()
	rnd := rand.NewChaCha8([32]byte{})
	ds := blob.NewStore()
	testdata := func(s string, n int64) blob.Data {
		buf := make([]byte, n)
		rnd.Read(buf)
		return blob.FromBytes(digest.SHA256, s, buf)
	}
	ds.Set(testdata("data 0", 512*1024))
	for i := 1; i < 15; i++ {
		ds.Set(testdata(fmt.Sprintf("data %d", i), 1024*1024))
	}
	sizeLimit := int64(10 * 1024 * 1024)
	numLimit := 10
	c := &Client{}
	blobsReqs, missingBlobs := c.blobsToUpload(ctx, ds.List(), ds, sizeLimit)
	batchReqs := createBatchUpdateBlobsRequests("projects/test/instances/default_instannce", digest.SHA256, blobsReqs, sizeLimit, numLimit)
	nBatches := 0
	for batchReq := range batchReqs {
		size := int64(proto.Size(batchReq))
		num := len(batchReq.Requests)
		t.Logf("size=%d num=%d", size, num)
		if size > sizeLimit || num > numLimit {
			t.Errorf("size=%d num=%d; exceeds limit size=%d num=%d", size, num, sizeLimit, numLimit)
		}
		nBatches++
	}
	if nBatches != 2 {
		t.Errorf("too many batch requests %d; want 2", nBatches)
	}
	if m := missingBlobs.Size(); m != 0 {
		t.Errorf("missingBlobs=%d; want=0", m)
	}
}

// TestCreateBatchUpdateBlobsRequestsDigestFunctionOverhead fills a batch to
// the brim: byteLimit is exactly the encoded size of one request holding all
// blobs but no digest_function field. The emitted requests carry
// digest_function, so their actual proto size must still stay within
// byteLimit (the batching must account for the field's encoded size).
func TestCreateBatchUpdateBlobsRequestsDigestFunctionOverhead(t *testing.T) {
	const instance = "projects/test/instances/default_instance"
	fn := digest.SHA256
	newReq := func(i byte) *rpb.BatchUpdateBlobsRequest_Request {
		data := bytes.Repeat([]byte{i}, 100)
		return &rpb.BatchUpdateBlobsRequest_Request{
			Digest: fn.FromBytes(data).Proto(),
			Data:   data,
		}
	}
	reqs := []*rpb.BatchUpdateBlobsRequest_Request{newReq(1), newReq(2)}
	byteLimit := int64(proto.Size(&rpb.BatchUpdateBlobsRequest{
		InstanceName: instance,
		Requests:     reqs,
	}))
	var nBatches, nReqs int
	for batchReq := range createBatchUpdateBlobsRequests(instance, fn, slices.Values(reqs), byteLimit, 100) {
		nBatches++
		nReqs += len(batchReq.Requests)
		if got := int64(proto.Size(batchReq)); got > byteLimit {
			t.Errorf("proto.Size(batch %d) = %d; want <= %d", nBatches, got, byteLimit)
		}
		if got, want := batchReq.DigestFunction, fn.Value(); got != want {
			t.Errorf("batch %d DigestFunction = %v; want %v", nBatches, got, want)
		}
	}
	if got, want := nReqs, len(reqs); got != want {
		t.Errorf("emitted %d blob requests across %d batches; want %d", got, nBatches, want)
	}
}

// newZstdTestClient builds a Client wired for newDecoder testing
// only: just the zstd decoder pool and an Option that selects ZSTD
// for any blob size. It is not safe for any RPC-using method.
func newZstdTestClient() *Client {
	pool := &sync.Pool{}
	pool.New = func() any {
		dec, err := zstd.NewReader(nil, zstdDecoderOpts...)
		if err != nil {
			panic(err)
		}
		return &pooledDecoder{Decoder: dec, pool: pool}
	}
	return &Client{
		opt: Option{
			compressor:     rpb.Compressor_ZSTD,
			CompressedBlob: 0,
		},
		zstdDecoderPool: pool,
	}
}

func zstdEncode(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	enc, err := zstd.NewWriter(&out)
	if err != nil {
		t.Fatalf("zstd.NewWriter: %v", err)
	}
	if _, err := enc.Write(data); err != nil {
		t.Fatalf("encode write: %v", err)
	}
	if err := enc.Close(); err != nil {
		t.Fatalf("encode close: %v", err)
	}
	return out.Bytes()
}

// zstdBlobOfCompressedSize returns a valid zstd stream of exactly
// target bytes plus the payload it decompresses to. A small data
// frame is followed by a skippable frame padded to the requested
// total length.
func zstdBlobOfCompressedSize(t *testing.T, target int, seed [32]byte) (compressed, payload []byte) {
	t.Helper()
	rnd := rand.NewChaCha8(seed)
	payload = make([]byte, 64)
	rnd.Read(payload)
	base := zstdEncode(t, payload)
	if len(base) > target {
		t.Fatalf("base compressed %d > target %d", len(base), target)
	}
	pad := target - len(base)
	if pad != 0 && pad < 8 {
		t.Fatalf("padding %d cannot fit a skippable frame header (8 bytes)", pad)
	}
	if pad == 0 {
		return base, payload
	}
	frame := make([]byte, pad)
	// 0x184D2A50 is one of zstd's skippable-frame magic numbers; the
	// decoder skips the frame's contents.
	binary.LittleEndian.PutUint32(frame[0:4], 0x184D2A50)
	binary.LittleEndian.PutUint32(frame[4:8], uint32(pad-8))
	rnd.Read(frame[8:])
	return append(base, frame...), payload
}

// Random data; compressed bytes stay under probeSize.
func TestNewDecoder_ZSTDPreBufferRoundTrip(t *testing.T) {
	c := newZstdTestClient()
	rnd := rand.NewChaCha8([32]byte{1})
	payload := make([]byte, probeSize/4)
	rnd.Read(payload)
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-prebuffer", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	t.Cleanup(func() { rd.Close() })

	if _, ok := rd.(*bufferedDecoder); !ok {
		t.Errorf("got %T; want *bufferedDecoder for blob below probe", rd)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// Random data; compressed bytes exceed probeSize so the MultiReader path streams.
func TestNewDecoder_ZSTDStreamingRoundTrip(t *testing.T) {
	c := newZstdTestClient()
	rnd := rand.NewChaCha8([32]byte{2})
	payload := make([]byte, freshDecoderThreshold+probeSize)
	rnd.Read(payload)
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-streaming", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	t.Cleanup(func() { rd.Close() })

	if _, ok := rd.(*discardableDecoder); !ok {
		t.Errorf("got %T; want *discardableDecoder for blob above probe", rd)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// Highly compressible blob: compressed bytes fit in the probe, but
// decompressed size meets freshDecoderThreshold. Must take the
// discardable path so the decoder's syncStream.dstBuf, which holds
// the entire decoded output after sync DecodeAll, can't end up in
// the pool.
func TestNewDecoder_ZSTDHighlyCompressibleLargeBlob(t *testing.T) {
	c := newZstdTestClient()
	payload := make([]byte, freshDecoderThreshold+probeSize)
	compressed := zstdEncode(t, payload)
	if len(compressed) > probeSize {
		t.Fatalf("expected compressed bytes <= probeSize; got %d", len(compressed))
	}

	d := digest.Digest{Hash: "test-compressible-large", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	t.Cleanup(func() { rd.Close() })

	if _, ok := rd.(*discardableDecoder); !ok {
		t.Errorf("got %T; want *discardableDecoder for compressed<=probe but decoded>=threshold", rd)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// Discardable decoders must drop, not pool: window-sized h.b would
// poison the pool. sync.Pool exposes no Put hook, so this test uses
// an indirect proxy: pooledDecoder.Close calls Reset(nil), leaving
// the underlying *zstd.Decoder usable; discardableDecoder.Close calls
// Decoder.Close, after which Reset(nil) returns ErrDecoderClosed.
func TestNewDecoder_DiscardableDoesNotPool(t *testing.T) {
	c := newZstdTestClient()
	rnd := rand.NewChaCha8([32]byte{3})
	payload := make([]byte, freshDecoderThreshold+probeSize)
	rnd.Read(payload)
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-no-pool", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	sd, ok := rd.(*discardableDecoder)
	if !ok {
		t.Fatalf("got %T; want *discardableDecoder", rd)
	}
	underlying := sd.Decoder

	if _, err := io.ReadAll(rd); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := underlying.Reset(nil); err == nil {
		t.Errorf("discardable decoder usable after Close; expected ErrDecoderClosed (would mean it was pooled, not dropped)")
	}
}

// Sync-path decoder must remain reusable after Close (i.e., return
// to the pool, not be dropped). sync.Pool exposes no Put hook, so
// this test uses the same indirect proxy as TestNewDecoder_DiscardableDoesNotPool:
// after Close, Reset(nil) on the underlying *zstd.Decoder must succeed
// (a Closed decoder would return ErrDecoderClosed).
func TestNewDecoder_PreBufferReturnsToPool(t *testing.T) {
	c := newZstdTestClient()
	payload := []byte("small payload, decoder should be pooled back")
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-pool", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	bd, ok := rd.(*bufferedDecoder)
	if !ok {
		t.Fatalf("got %T; want *bufferedDecoder", rd)
	}
	underlying := bd.Decoder

	if _, err := io.ReadAll(rd); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := underlying.Reset(nil); err != nil {
		t.Errorf("pre-buffer decoder Closed after Close; should remain pool-usable: %v", err)
	}
}

func TestNewDecoder_IdentityPassthrough(t *testing.T) {
	c := newZstdTestClient()
	c.opt.compressor = rpb.Compressor_IDENTITY

	payload := []byte("hello world")
	d := digest.Digest{Hash: "test-identity", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(payload), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	t.Cleanup(func() { rd.Close() })

	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("passthrough mismatch: got %q, want %q", got, payload)
	}
}

func TestBufferedDecoder_CloseReleasesBuffer(t *testing.T) {
	c := newZstdTestClient()
	payload := []byte("small payload for lifetime check")
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-lifetime", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	bd, ok := rd.(*bufferedDecoder)
	if !ok {
		t.Fatalf("got %T; want *bufferedDecoder", rd)
	}
	if bd.buf == nil {
		t.Fatal("bufferedDecoder.buf is nil before Close")
	}
	if _, err := io.ReadAll(rd); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if bd.buf != nil {
		t.Errorf("bufferedDecoder.buf not nilled by Close; got %p", bd.buf)
	}
}

// A blob whose compressed length equals probeSize must take the sync
// path. CopyN with a probeSize limit would never see EOF on a reader
// holding exactly that many bytes; reading probeSize+1 catches it.
func TestNewDecoder_ZSTDExactProbeSize(t *testing.T) {
	c := newZstdTestClient()
	compressed, payload := zstdBlobOfCompressedSize(t, probeSize, [32]byte{99})
	if len(compressed) != probeSize {
		t.Fatalf("blob length %d != probeSize %d", len(compressed), probeSize)
	}

	d := digest.Digest{Hash: "test-exact-probe", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	t.Cleanup(func() { rd.Close() })

	if _, ok := rd.(*bufferedDecoder); !ok {
		t.Errorf("got %T; want *bufferedDecoder for compressed size = probeSize", rd)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// A blob one byte over probeSize must take the streaming branch. With
// d.SizeBytes below freshDecoderThreshold it shares a pool decoder
// (bufferedDecoder); the *discardableDecoder fresh-decoder path is
// covered by TestNewDecoder_ZSTDStreamingRoundTrip.
func TestNewDecoder_ZSTDProbeSizePlusOne(t *testing.T) {
	c := newZstdTestClient()
	compressed, payload := zstdBlobOfCompressedSize(t, probeSize+1, [32]byte{100})
	if len(compressed) != probeSize+1 {
		t.Fatalf("blob length %d != probeSize+1 %d", len(compressed), probeSize+1)
	}

	d := digest.Digest{Hash: "test-probe-plus-one", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	t.Cleanup(func() { rd.Close() })

	if _, ok := rd.(*bufferedDecoder); !ok {
		t.Errorf("got %T; want *bufferedDecoder for streaming via pool decoder", rd)
	}
	got, err := io.ReadAll(rd)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("decoded mismatch: got %d bytes, want %d", len(got), len(payload))
	}
}

// errAfterReader returns data then a custom error on the next Read.
// Used to drive the io.CopyN error branch in newDecoder.
type errAfterReader struct {
	data []byte
	err  error
	off  int
}

func (r *errAfterReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, r.err
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}

// A non-EOF error from the source mid-probe must propagate from
// newDecoder. Probe buf is recycled even on this path.
func TestNewDecoder_ZSTDProbeReadError(t *testing.T) {
	c := newZstdTestClient()
	payload := []byte("payload that won't be reached")
	compressed := zstdEncode(t, payload)
	wantErr := errors.New("synthetic mid-probe read error")
	r := &errAfterReader{data: compressed[:len(compressed)/2], err: wantErr}

	d := digest.Digest{Hash: "test-probe-error", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(r, d)
	if rd != nil {
		t.Errorf("got non-nil decoder %T on error path", rd)
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("got err=%v; want %v", err, wantErr)
	}
}

// Close must be idempotent: callers may double-Close in error/defer
// paths and a second call must not double-Put the pooled decoder or
// NPE on the recycled buf.
func TestNewDecoder_DoubleCloseBuffered(t *testing.T) {
	c := newZstdTestClient()
	payload := []byte("small payload, idempotent Close check")
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-double-close-buffered", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	if _, ok := rd.(*bufferedDecoder); !ok {
		t.Fatalf("got %T; want *bufferedDecoder", rd)
	}
	if _, err := io.ReadAll(rd); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Errorf("second Close should be a safe no-op; got %v", err)
	}
}

func TestNewDecoder_DoubleCloseDiscardable(t *testing.T) {
	c := newZstdTestClient()
	rnd := rand.NewChaCha8([32]byte{42})
	payload := make([]byte, freshDecoderThreshold+probeSize)
	rnd.Read(payload)
	compressed := zstdEncode(t, payload)

	d := digest.Digest{Hash: "test-double-close-discardable", SizeBytes: int64(len(payload))}
	rd, err := c.newDecoder(bytes.NewReader(compressed), d)
	if err != nil {
		t.Fatalf("newDecoder: %v", err)
	}
	if _, ok := rd.(*discardableDecoder); !ok {
		t.Fatalf("got %T; want *discardableDecoder", rd)
	}
	if _, err := io.ReadAll(rd); err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := rd.Close(); err != nil {
		t.Errorf("second Close should be a safe no-op; got %v", err)
	}
}

func TestCreateBatchUpdateBlobsRequestsWithCompression(t *testing.T) {
	ctx := t.Context()
	rnd := rand.NewChaCha8([32]byte{})
	ds := blob.NewStore()
	testdata := func(s string, n int64) blob.Data {
		buf := make([]byte, n)
		rnd.Read(buf)
		return blob.FromBytes(digest.SHA256, s, buf)
	}
	ds.Set(testdata("data 0", 1023))
	ds.Set(testdata("data 1", 1024))
	ds.Set(testdata("data 2", 1025))
	sizeLimit := int64(3*1024 - 1)
	numLimit := 10
	c := &Client{
		opt: Option{
			BatchCompressedBlob:           1024,
			compressorForBatchUpdateBlobs: rpb.Compressor_ZSTD,
		},
	}
	blobsReqsTemp, missingBlobs := c.blobsToUpload(ctx, ds.List(), ds, sizeLimit)
	blobsReqsSlice := slices.Collect(blobsReqsTemp)
	numBlobsReqs := len(blobsReqsSlice)
	blobsReqs := slices.Values(blobsReqsSlice)
	batchReqs := createBatchUpdateBlobsRequests("projects/test/instances/default_instannce", digest.SHA256, blobsReqs, sizeLimit, numLimit)
	var blobsPerBatch []int
	numCompressedBlobs := 0
	for batchReq := range batchReqs {
		blobsPerBatch = append(blobsPerBatch, len(batchReq.Requests))
		for _, req := range batchReq.Requests {
			if req.Compressor == rpb.Compressor_ZSTD {
				numCompressedBlobs++
			}
		}
	}
	numBatches := len(blobsPerBatch)
	if numBlobsReqs != 3 {
		t.Errorf("numBlobsReqs=%d, want=3", numBlobsReqs)
	}
	if numBatches != 2 {
		t.Errorf("numBatches=%d, want=2", numBatches)
	}
	if blobsPerBatch[0] != 2 {
		t.Errorf("blobsPerBatch[0]=%d, want=2", blobsPerBatch[0])
	}
	if blobsPerBatch[1] != 1 {
		t.Errorf("blobsPerBatch[1]=%d, want=1", blobsPerBatch[1])
	}
	if numCompressedBlobs != 2 {
		t.Errorf("numCompressedBlobs=%d, want=2", numCompressedBlobs)
	}
	if m := missingBlobs.Size(); m != 0 {
		t.Errorf("missingBlobs=%d; want=0", m)
	}
}

type fakeBatchReadCAS struct {
	rpb.UnimplementedContentAddressableStorageServer

	mu   sync.Mutex
	reqs []*rpb.BatchReadBlobsRequest
	fn   func(ctx context.Context, req *rpb.BatchReadBlobsRequest, call int) (*rpb.BatchReadBlobsResponse, error)
}

func (f *fakeBatchReadCAS) BatchReadBlobs(ctx context.Context, req *rpb.BatchReadBlobsRequest) (*rpb.BatchReadBlobsResponse, error) {
	f.mu.Lock()
	call := len(f.reqs)
	f.reqs = append(f.reqs, req)
	fn := f.fn
	f.mu.Unlock()
	return fn(ctx, req, call)
}

func (f *fakeBatchReadCAS) requests() []*rpb.BatchReadBlobsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.reqs)
}

func newBatchReadTestClient(t *testing.T, casSrv rpb.ContentAddressableStorageServer, opt Option) *Client {
	t.Helper()
	ctx := t.Context()
	lis := bufconn.Listen(1 << 20)
	srv := grpc.NewServer()
	rpb.RegisterContentAddressableStorageServer(srv, casSrv)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	if opt.Instance == "" {
		opt.Instance = "projects/test/instances/default"
	}
	c, err := NewFromConn(ctx, opt, cred.Cred{}, conn, conn, conn)
	if err != nil {
		conn.Close()
		t.Fatalf("NewFromConn: %v", err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("Client.Close: %v", err)
		}
	})
	return c
}

func flateEncode(t *testing.T, data []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		t.Fatalf("flate.NewWriter: %v", err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatalf("flate write: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("flate close: %v", err)
	}
	return out.Bytes()
}

func TestGetWithBatchReadBlobs(t *testing.T) {
	payload := []byte("hello batch read blobs test payload")
	d := digest.SHA256.FromBytes(payload)
	zstdData := zstdEncode(t, payload)
	deflateData := flateEncode(t, payload)

	for _, tc := range []struct {
		name            string
		opt             Option
		fn              func(ctx context.Context, req *rpb.BatchReadBlobsRequest, call int) (*rpb.BatchReadBlobsResponse, error)
		wantCalls       int
		wantCompressors []rpb.Compressor_Value
	}{
		{
			name: "identity_nil_status",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest:     req.Digests[0],
							Data:       payload,
							Compressor: rpb.Compressor_IDENTITY,
						},
					},
				}, nil
			},
			wantCalls: 1,
		},
		{
			name: "zstd",
			opt: Option{
				BatchCompressedBlob: 1,
			},
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest:     req.Digests[0],
							Data:       zstdData,
							Compressor: rpb.Compressor_ZSTD,
							Status:     &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCalls:       1,
			wantCompressors: []rpb.Compressor_Value{rpb.Compressor_ZSTD, rpb.Compressor_DEFLATE},
		},
		{
			name: "deflate",
			opt: Option{
				BatchCompressedBlob: 1,
			},
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest:     req.Digests[0],
							Data:       deflateData,
							Compressor: rpb.Compressor_DEFLATE,
							Status:     &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCalls:       1,
			wantCompressors: []rpb.Compressor_Value{rpb.Compressor_ZSTD, rpb.Compressor_DEFLATE},
		},
		{
			name: "rpc_error_retriable_then_ok",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, call int) (*rpb.BatchReadBlobsResponse, error) {
				if call == 0 {
					return nil, status.Error(codes.Unavailable, "transient error")
				}
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Data:   payload,
							Status: &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCalls: 2,
		},
		{
			name: "response_status_retriable_then_ok",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, call int) (*rpb.BatchReadBlobsResponse, error) {
				if call == 0 {
					return &rpb.BatchReadBlobsResponse{
						Responses: []*rpb.BatchReadBlobsResponse_Response{
							{
								Digest: req.Digests[0],
								Status: &statuspb.Status{
									Code:    int32(codes.Unavailable),
									Message: "temporarily unavailable",
								},
							},
						},
					}, nil
				}
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Data:   payload,
							Status: &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCalls: 2,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fake := &fakeBatchReadCAS{fn: tc.fn}
			c := newBatchReadTestClient(t, fake, tc.opt)

			got, err := c.getWithBatchReadBlobs(ctx, d, "test-blob")
			if err != nil {
				t.Fatalf("getWithBatchReadBlobs() error = %v; want nil", err)
			}
			if !bytes.Equal(got, payload) {
				t.Errorf("getWithBatchReadBlobs() = %q; want %q", got, payload)
			}

			reqs := fake.requests()
			if len(reqs) != tc.wantCalls {
				t.Fatalf("BatchReadBlobs calls = %d; want %d", len(reqs), tc.wantCalls)
			}
			req := reqs[0]
			if req.GetInstanceName() != c.opt.Instance {
				t.Errorf("InstanceName = %q; want %q", req.GetInstanceName(), c.opt.Instance)
			}
			if len(req.GetDigests()) != 1 || digest.FromProto(req.Digests[0]) != d {
				t.Errorf("Digests = %v; want [%v]", req.GetDigests(), d)
			}
			if req.GetDigestFunction() != c.digestFn.Value() {
				t.Errorf("DigestFunction = %v; want %v", req.GetDigestFunction(), c.digestFn.Value())
			}
			if !slices.Equal(req.GetAcceptableCompressors(), tc.wantCompressors) {
				t.Errorf("AcceptableCompressors = %v; want %v", req.GetAcceptableCompressors(), tc.wantCompressors)
			}

			stats := c.m.Stats()
			if stats.ROps != 1 || stats.RErrs != 0 || stats.RBytes != int64(len(payload)) {
				t.Errorf("iometrics = (ROps=%d, RErrs=%d, RBytes=%d); want (1, 0, %d)", stats.ROps, stats.RErrs, stats.RBytes, len(payload))
			}
		})
	}
}

func TestGetWithBatchReadBlobs_Error(t *testing.T) {
	payload := []byte("hello batch read blobs test payload")
	d := digest.SHA256.FromBytes(payload)

	for _, tc := range []struct {
		name            string
		fn              func(ctx context.Context, req *rpb.BatchReadBlobsRequest, call int) (*rpb.BatchReadBlobsResponse, error)
		wantCode        codes.Code
		wantErrIs       error
		wantErrContains string
	}{
		{
			name: "rpc_error_non_retriable",
			fn: func(_ context.Context, _ *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return nil, status.Error(codes.InvalidArgument, "bad request")
			},
			wantCode: codes.InvalidArgument,
		},
		{
			name: "empty_responses",
			fn: func(_ context.Context, _ *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{}, nil
			},
			wantCode:        codes.Unknown,
			wantErrContains: "responses=0",
		},
		{
			name: "too_many_responses",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				entry := &rpb.BatchReadBlobsResponse_Response{
					Digest: req.Digests[0],
					Data:   payload,
				}
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{entry, entry},
				}, nil
			},
			wantCode:        codes.Unknown,
			wantErrContains: "responses=2",
		},
		{
			name: "response_status_not_found",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Status: &statuspb.Status{
								Code:    int32(codes.NotFound),
								Message: "blob not found",
							},
						},
					},
				}, nil
			},
			wantCode: codes.NotFound,
		},
		{
			name: "response_status_error_with_data",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Data:   payload,
							Status: &statuspb.Status{
								Code:    int32(codes.FailedPrecondition),
								Message: "precondition failed",
							},
						},
					},
				}, nil
			},
			wantCode: codes.FailedPrecondition,
		},
		{
			name: "decode_truncated_data",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Data:   payload[:len(payload)-1],
							Status: &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCode:  codes.Unknown,
			wantErrIs: io.ErrUnexpectedEOF,
		},
		{
			name: "decode_trailing_data",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Data:   append(slices.Clone(payload), '!'),
							Status: &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCode:        codes.Unknown,
			wantErrContains: "exceeds expected size",
		},
		{
			name: "decode_corrupt_zstd",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest:     req.Digests[0],
							Data:       []byte("not valid zstd data"),
							Compressor: rpb.Compressor_ZSTD,
							Status:     &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCode:        codes.Unknown,
			wantErrContains: "failed to decompress blobs",
		},
		{
			name: "decode_unsupported_compressor",
			fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest:     req.Digests[0],
							Data:       payload,
							Compressor: rpb.Compressor_Value(99),
							Status:     &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			},
			wantCode:        codes.Unknown,
			wantErrContains: "unsupported compressor",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			fake := &fakeBatchReadCAS{fn: tc.fn}
			c := newBatchReadTestClient(t, fake, Option{})

			got, err := c.getWithBatchReadBlobs(ctx, d, "test-blob")
			if err == nil {
				t.Fatalf("getWithBatchReadBlobs() = %q, nil; want error", got)
			}
			if reqs := fake.requests(); len(reqs) != 1 {
				t.Errorf("BatchReadBlobs calls = %d; want 1", len(reqs))
			}
			if status.Code(err) != tc.wantCode {
				t.Errorf("status.Code(err) = %v (err=%v); want %v", status.Code(err), err, tc.wantCode)
			}
			if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
				t.Errorf("err = %v; want errors.Is(_, %v)", err, tc.wantErrIs)
			}
			if tc.wantErrContains != "" && !strings.Contains(err.Error(), tc.wantErrContains) {
				t.Errorf("err = %q; want substring %q", err.Error(), tc.wantErrContains)
			}
			if stats := c.m.Stats(); stats.ROps != 1 || stats.RErrs != 1 || stats.RBytes != 0 {
				t.Errorf("iometrics = (ROps=%d, RErrs=%d, RBytes=%d); want (1, 1, 0)", stats.ROps, stats.RErrs, stats.RBytes)
			}
		})
	}
}

func TestGetWithBatchReadBlobs_LocalCache(t *testing.T) {
	ctx := t.Context()
	payload := []byte("cached batch read payload")
	d := digest.SHA256.FromBytes(payload)
	missing := digest.SHA256.FromBytes([]byte("missing blob"))

	fake := &fakeBatchReadCAS{
		fn: func(_ context.Context, req *rpb.BatchReadBlobsRequest, _ int) (*rpb.BatchReadBlobsResponse, error) {
			if digest.FromProto(req.Digests[0]) == d {
				return &rpb.BatchReadBlobsResponse{
					Responses: []*rpb.BatchReadBlobsResponse_Response{
						{
							Digest: req.Digests[0],
							Data:   payload,
							Status: &statuspb.Status{Code: int32(codes.OK)},
						},
					},
				}, nil
			}
			return &rpb.BatchReadBlobsResponse{
				Responses: []*rpb.BatchReadBlobsResponse_Response{
					{
						Digest: req.Digests[0],
						Status: &statuspb.Status{
							Code:    int32(codes.NotFound),
							Message: "not found",
						},
					},
				},
			}, nil
		},
	}

	lc, err := NewLocalCache(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalCache: %v", err)
	}
	c := newBatchReadTestClient(t, fake, Option{LocalCache: lc})

	// Cache miss: fetches from CAS and populates local cache.
	got, err := c.getWithBatchReadBlobs(ctx, d, "cached-blob")
	if err != nil {
		t.Fatalf("first getWithBatchReadBlobs: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("first getWithBatchReadBlobs = %q; want %q", got, payload)
	}
	if calls := len(fake.requests()); calls != 1 {
		t.Fatalf("BatchReadBlobs calls after miss = %d; want 1", calls)
	}
	if !lc.HasContent(ctx, d) {
		t.Errorf("LocalCache.HasContent(%s) = false; want true", d)
	}

	// Cache hit: served from local cache without another RPC.
	got, err = c.getWithBatchReadBlobs(ctx, d, "cached-blob")
	if err != nil {
		t.Fatalf("second getWithBatchReadBlobs: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("second getWithBatchReadBlobs = %q; want %q", got, payload)
	}
	if calls := len(fake.requests()); calls != 1 {
		t.Errorf("BatchReadBlobs calls after hit = %d; want 1", calls)
	}

	// Error response: not written to local cache.
	if _, err := c.getWithBatchReadBlobs(ctx, missing, "missing-blob"); status.Code(err) != codes.NotFound {
		t.Errorf("getWithBatchReadBlobs(missing) = %v; want NotFound", err)
	}
	if lc.HasContent(ctx, missing) {
		t.Errorf("LocalCache.HasContent(%s) = true after error; want false", missing)
	}
}
