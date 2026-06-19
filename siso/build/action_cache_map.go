// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/reapi"
	"go.chromium.org/build/siso/reapi/digest"
)

type actionCacheMap interface {
	Add(context.Context, string, digest.Digest) error
	List(context.Context, string) iter.Seq2[*rpb.Action, error]
}

// -- experiment implementation using local disk

type localActionCacheMap struct {
	dir         string
	reapiclient *reapi.Client
}

func (m localActionCacheMap) Add(ctx context.Context, lookupKey string, action digest.Digest) error {
	if lookupKey == "" {
		return errors.New("lookupkey is empty")
	}
	if action.IsZero() {
		return errors.New("action is zero digest")
	}
	fname := filepath.Join(m.dir, strings.ReplaceAll(lookupKey, "/", "-"), fmt.Sprintf("%s-%d", action.Hash, action.SizeBytes))
	err := os.MkdirAll(filepath.Dir(fname), 0755)
	if err != nil {
		return fmt.Errorf("mkdir for %q: %w", fname, err)
	}
	err = os.WriteFile(fname, nil, 0644)
	if err != nil {
		return fmt.Errorf("add %q in action cache map: %w", action, err)
	}
	return nil
}

func (m localActionCacheMap) List(ctx context.Context, lookupKey string) iter.Seq2[*rpb.Action, error] {
	ctx, span := trace.NewSpan(ctx, "local-action-cache-map-list")
	defer span.Close(nil)

	dname := filepath.Join(m.dir, strings.ReplaceAll(lookupKey, "/", "-"))
	d, err := os.Open(dname)
	if err != nil {
		return func(yield func(*rpb.Action, error) bool) {
			yield(nil, err)
		}
	}
	defer d.Close()
	names, err := d.Readdirnames(-1)
	if err != nil {
		return func(yield func(*rpb.Action, error) bool) {
			yield(nil, err)
		}
	}
	return func(yield func(*rpb.Action, error) bool) {
		for _, name := range names {
			d, err := digest.Parse(strings.Replace(name, "-", "/", 1))
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			action := &rpb.Action{}
			err = m.reapiclient.Proto(ctx, d, action)
			if err != nil {
				if !yield(nil, err) {
					return
				}
				continue
			}
			if !yield(action, nil) {
				return
			}
		}
	}

}
