// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/execute/localexec"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/path"
)

func (b *Builder) tapCanonicalizeCmd(ctx context.Context, cmd *execute.Cmd) error {
	res, _ := cmd.ActionResult()
	tapData, tapped := localexec.ExtractTapResult(res)
	if !tapped {
		return errors.New("no tap data")
	}
	var ninputs, noutputs int
	// ignore out of workspace root
	// TODO: use with input root absolute path?
	// TODO: just use detected inputs?
	seen := make(map[string]bool)
	for _, input := range cmd.AllInputs() {
		seen[string(input)] = true
		ninputs++
	}
	for _, input := range tapData.Reads {
		rel, err := filepath.Rel(b.path.WorkspaceRoot, input)
		if err != nil {
			clog.Warningf(ctx, "reads relpath %q: %v", input, err)
			continue
		}
		if !filepath.IsLocal(rel) {
			continue
		}
		_, err = b.hashFS.Stat(ctx, b.path.WorkspaceRoot, path.New(rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		cmd.Inputs = append(cmd.Inputs, path.New(rel))
	}
	clear(seen)
	// need to use both original outputs and detected outputs.
	// it might not detect output for restat action.
	// it might detect unspecified outputs.
	for _, output := range cmd.AllOutputs() {
		seen[string(output)] = true
		noutputs++
	}
	for _, output := range tapData.Writes {
		rel, err := filepath.Rel(b.path.WorkspaceRoot, output)
		if err != nil {
			clog.Warningf(ctx, "writes relpath %q: %v", output, err)
			continue
		}
		if !filepath.IsLocal(rel) {
			continue
		}
		fi, err := b.hashFS.Stat(ctx, b.path.WorkspaceRoot, path.New(rel))
		if err != nil {
			continue
		}
		if fi.IsDir() {
			// don't include output directories
			// as it would forget all entries in the directory
			// by hashfs Update.
			continue
		}
		if seen[rel] {
			continue
		}
		seen[rel] = true
		cmd.Outputs = append(cmd.Outputs, path.New(rel))
	}
	for _, del := range tapData.Deletes {
		rel, err := filepath.Rel(b.path.WorkspaceRoot, del)
		if err != nil {
			clog.Warningf(ctx, "deletes relpath %q: %v", del, err)
			continue
		}
		if !filepath.IsLocal(rel) {
			continue
		}
		relPath := path.New(rel)
		_, err = b.hashFS.Stat(ctx, b.path.WorkspaceRoot, relPath)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		b.hashFS.Forget(ctx, b.path.WorkspaceRoot, []path.Path{relPath})
		clog.Infof(ctx, "delete %q", rel)
	}

	// now tap results are applied to cmd, so we can consider
	// this cmd is pure, thus cacheable.
	cmd.Pure = true
	clog.Infof(ctx, "tap canonicalized inputs=%d->%d outputs=%d->%d", ninputs, len(cmd.Inputs), noutputs, len(cmd.Outputs))
	return nil
}
