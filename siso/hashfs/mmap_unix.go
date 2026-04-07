// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build unix

package hashfs

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// mmapReadFile maps a file into memory as a read-only byte slice.
// The returned byte slice is backed by the OS page cache, not the Go heap.
// The caller must call munmapFile when done with the data.
func mmapReadFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := fi.Size()
	if size == 0 {
		return nil, fmt.Errorf("file %s is empty", path)
	}

	data, err := unix.Mmap(int(f.Fd()), 0, int(size), unix.PROT_READ, unix.MAP_PRIVATE)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", path, err)
	}
	return data, nil
}

// munmapFile unmaps a previously mmap'd byte slice.
func munmapFile(data []byte) error {
	return unix.Munmap(data)
}

// mmapWriteFile truncates f to the given size and maps it into memory as a
// read-write byte slice. The caller writes into the returned slice, then
// calls the returned closer to unmap and close the file.
func mmapWriteFile(f *os.File, size int) (data []byte, closer func() error, retErr error) {
	defer func() {
		if retErr != nil {
			f.Close()
		}
	}()

	if err := f.Truncate(int64(size)); err != nil {
		return nil, nil, fmt.Errorf("truncate %s: %w", f.Name(), err)
	}

	data, err := unix.Mmap(int(f.Fd()), 0, size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("mmap %s: %w", f.Name(), err)
	}

	closer = func() error {
		if err := unix.Munmap(data); err != nil {
			f.Close()
			return fmt.Errorf("munmap %s: %w", f.Name(), err)
		}
		return f.Close()
	}
	return data, closer, nil
}
