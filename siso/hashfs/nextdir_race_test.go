// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"io/fs"
	"sync"
	"testing"
)

// TestNextDirErrNotExistRace checks that storing a file creates its
// missing parent directory while another goroutine keeps recording that
// directory as missing.
func TestNextDirErrNotExistRace(t *testing.T) {
	ctx := t.Context()
	for i := range 10000 {
		root := &directory{isRoot: true}
		stop := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			for {
				select {
				case <-stop:
					return
				default:
				}
				missing := newLocalEntry()
				missing.err = fs.ErrNotExist
				root.store(ctx, "sysroot", missing)
			}
		})
		_, _, err := root.store(ctx, "sysroot/usr/lib", newLocalEntry())
		close(stop)
		wg.Wait()
		if err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
	}
}
