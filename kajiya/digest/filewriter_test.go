package digest

import (
	"os"
	"path/filepath"
	"testing"
)

func TestHashingFileWriter(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "testfile")

	w, err := NewHashingFileWriter(path)
	if err != nil {
		t.Fatalf("NewHashingFileWriter(%q) failed: %v", path, err)
	}

	data := []byte("hello world")
	n, err := w.Write(data)
	if err != nil {
		t.Errorf("Write failed: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write returned n=%d, want %d", n, len(data))
	}

	if w.Size() != int64(len(data)) {
		t.Errorf("Size() = %d, want %d", w.Size(), len(data))
	}

	expectedHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9" // SHA256 of "hello world"
	if w.Hash() != expectedHash {
		t.Errorf("Hash() = %q, want %q", w.Hash(), expectedHash)
	}

	d := w.Digest()
	if d.Hash != expectedHash {
		t.Errorf("Digest().Hash = %q, want %q", d.Hash, expectedHash)
	}
	if d.Size != int64(len(data)) {
		t.Errorf("Digest().Size = %d, want %d", d.Size, len(data))
	}

	if err := w.Close(); err != nil {
		t.Errorf("Close failed: %v", err)
	}

	// Verify file content
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("File content = %q, want %q", string(got), string(data))
	}
}

func TestHashingFileWriter_MultipleWrites(t *testing.T) {
	tmpDir := t.TempDir()
	path := filepath.Join(tmpDir, "testfile")

	w, err := NewHashingFileWriter(path)
	if err != nil {
		t.Fatalf("NewHashingFileWriter(%q) failed: %v", path, err)
	}

	data1 := []byte("hello ")
	data2 := []byte("world")

	n, err := w.Write(data1)
	if n != len(data1) {
		t.Errorf("Write() returned n=%d, want %d", n, len(data1))
	}
	if err != nil {
		t.Errorf("Write() failed: %v", err)
	}

	n, err = w.Write(data2)
	if n != len(data2) {
		t.Errorf("Write() returned n=%d, want %d", n, len(data2))
	}
	if err != nil {
		t.Errorf("Write() failed: %v", err)
	}

	expectedHash := "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"
	if w.Hash() != expectedHash {
		t.Errorf("Hash() = %q, want %q", w.Hash(), expectedHash)
	}

	if w.Size() != int64(len(data1)+len(data2)) {
		t.Errorf("Size() = %d, want %d", w.Size(), len(data1)+len(data2))
	}

	err = w.Close()
	if err != nil {
		t.Errorf("Close() failed: %v", err)
	}
}
