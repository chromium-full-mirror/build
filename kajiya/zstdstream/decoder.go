package zstdstream

import (
	"io"
	"io/fs"
	"log/slog"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var decoderPool = sync.Pool{
	New: func() any {
		d, err := zstd.NewReader(nil)
		if err != nil {
			panic(err)
		}
		return d
	},
}

// DecompressingWriter is a Writer that decompresses data and writes the result to an underlying
// WriteCloser. It uses a pipe and a background goroutine for streamed decompression.
type DecompressingWriter struct {
	pipeWriter *io.PipeWriter
	decCh      chan error
}

// NewDecompressingWriter creates a DecompressingWriter that decompresses Zstd encoded data written
// to it and writes the resulting bytes to the provided io.Writer.
func NewDecompressingWriter(dst io.Writer) (*DecompressingWriter, error) {
	pipeReader, pipeWriter := io.Pipe()
	decoder := decoderPool.Get().(*zstd.Decoder)
	err := decoder.Reset(pipeReader)
	if err != nil {
		return nil, err
	}
	decCh := make(chan error, 1)
	go func() {
		_, err := decoder.WriteTo(dst)
		_ = pipeReader.CloseWithError(err)
		decCh <- err
		close(decCh)

		// Reset decoder for reuse.
		if err := decoder.Reset(nil); err != nil {
			slog.Error("failed to reset decoder", "error", err)
		} else {
			decoderPool.Put(decoder)
		}
	}()

	return &DecompressingWriter{
		pipeWriter: pipeWriter,
		decCh:      decCh,
	}, nil
}

// Write decompresses the provided bytes and writes the result to the underlying stream.
// Calling Write after Close will fail with fs.ErrClosedPipe.
func (d *DecompressingWriter) Write(p []byte) (n int, err error) {
	if d == nil {
		return 0, fs.ErrInvalid
	}
	return d.pipeWriter.Write(p)
}

// Close closes the pipe writer, then waits for the decompression goroutine to finish and returns
// any error from the decompression.
func (d *DecompressingWriter) Close() error {
	if d == nil {
		return fs.ErrInvalid
	}
	if err := d.pipeWriter.Close(); err != nil {
		return err
	}
	return <-d.decCh
}
