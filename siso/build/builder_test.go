// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"go.chromium.org/build/hashigo/digest"
	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/blob"
	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
	"go.chromium.org/build/siso/reapi/reapitest"
)

// TestOutputs_OutputLocal_UpdatesTargets verifies that (*Builder).outputs
// updates b.targets and flushes outputs to disk even when step.def.LocalOutputs
// lists the step's outputs (output_local=true). Without this, a stale
// targetState entry populated by an earlier step with a stale .siso_deps entry
// remained untouched, causing downstream consumers to skip rebuilding
// (b/590907111).
func TestOutputs_OutputLocal_UpdatesTargets(t *testing.T) {
	for _, tc := range []struct {
		name           string
		outputLocalOpt bool
	}{
		{
			name:           "output_local_strategy_minimum",
			outputLocalOpt: false,
		},
		{
			name:           "output_local_strategy_full",
			outputLocalOpt: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			cleanupCtx := context.WithoutCancel(ctx)
			env := newRacingTestEnv(t, &reapitest.Fake{})
			var needFlushCalls []string
			hfs, err := hashfs.New(ctx, hashfs.Option{
				OutputLocal: func(_ context.Context, fname string) bool {
					needFlushCalls = append(needFlushCalls, fname)
					return tc.outputLocalOpt
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = hfs.Close(cleanupCtx) })
			b := env.newBuilder(t, func(opts *Options) {
				opts.HashFS = hfs
			})

			outRel := path.Path("out/siso/gen/header.h")
			fullOut := filepath.Join(env.dir, string(outRel))
			if err := os.MkdirAll(filepath.Dir(fullOut), 0755); err != nil {
				t.Fatal(err)
			}
			oldContent := []byte("// v1\n")
			if err := os.WriteFile(fullOut, oldContent, 0644); err != nil {
				t.Fatal(err)
			}
			oldTime := time.Now().Add(-1 * time.Hour).Truncate(time.Second)
			err = hfs.Update(ctx, env.dir, []hashfs.UpdateEntry{{
				Name: outRel,
				Entry: &merkletree.Entry{
					Name: outRel,
					Data: blob.FromBytes(digest.SHA256, string(outRel), oldContent),
				},
				IsLocal:     true,
				IsChanged:   false,
				Mode:        0644,
				ModTime:     oldTime,
				UpdatedTime: oldTime,
				CmdHash:     []byte("old-cmdhash"),
				EdgeHash:    []byte("old-edgehash"),
			}})
			if err != nil {
				t.Fatal(err)
			}

			// Simulate an earlier step whose stale .siso_deps references outRel
			// before the remote/cached output_local generator step runs. This
			// caches {mtime: oldTime, changed: false} in b.targets.
			consumerDef := fakeStepDef{
				actionName:    "cxx",
				triggerInputs: []path.Path{outRel},
			}
			_, mtime, err := inputMtime(ctx, b, consumerDef)
			if err != nil {
				t.Fatalf("initial inputMtime() err=%v; want nil", err)
			}
			if !mtime.Equal(oldTime) {
				t.Fatalf("initial inputMtime() mtime=%v; want %v", mtime, oldTime)
			}

			// Simulate the remote/cached output_local generator step recording
			// its updated file and directory outputs in HashFS (not yet flushed
			// to local disk) and running (*Builder).outputs.
			newContent := []byte("// v2\n")
			newTime := oldTime.Add(10 * time.Minute)
			dirRel := path.Path("out/siso/gen/dir_out")
			dirMemberRel := path.Path("out/siso/gen/dir_out/nested.h")
			dirMemberTime := newTime.Add(5 * time.Minute)
			actionDigest := digest.Digest{Hash: "new-action", SizeBytes: 1}
			err = hfs.Update(ctx, env.dir, []hashfs.UpdateEntry{
				{
					Name: outRel,
					Entry: &merkletree.Entry{
						Name: outRel,
						Data: blob.FromBytes(digest.SHA256, string(outRel), newContent),
					},
					IsLocal:     false,
					IsChanged:   true,
					Mode:        0644,
					ModTime:     newTime,
					UpdatedTime: newTime,
					CmdHash:     []byte("new-cmdhash"),
					EdgeHash:    []byte("new-edgehash"),
					Action:      actionDigest,
				},
				{
					Name:        dirRel,
					Entry:       &merkletree.Entry{Name: dirRel},
					IsLocal:     false,
					IsChanged:   true,
					Mode:        fs.ModeDir | 0755,
					ModTime:     oldTime,
					UpdatedTime: oldTime,
					CmdHash:     []byte("new-cmdhash"),
					EdgeHash:    []byte("new-edgehash"),
					Action:      actionDigest,
				},
				{
					Name: dirMemberRel,
					Entry: &merkletree.Entry{
						Name: dirMemberRel,
						Data: blob.FromBytes(digest.SHA256, string(dirMemberRel), newContent),
					},
					IsLocal:     false,
					IsChanged:   true,
					Mode:        0644,
					ModTime:     dirMemberTime,
					UpdatedTime: dirMemberTime,
					CmdHash:     []byte("new-cmdhash"),
					Action:      actionDigest,
				},
			})
			if err != nil {
				t.Fatal(err)
			}

			genCmd := &execute.Cmd{
				HashFS:        hfs,
				WorkspaceRoot: env.dir,
				Outputs:       []path.Path{outRel},
				OutputDirs:    []path.Path{dirRel},
			}
			genCmd.SetActionResult(&rpb.ActionResult{ExitCode: 0}, false)
			genStep := &Step{
				outputPaths:    []string{"gen/header.h", "gen/dir_out/"},
				cmd:            genCmd,
				state:          &stepState{},
				startReported:  new(sync.Once),
				finishReported: new(sync.Once),
				def: fakeStepDef{
					actionName:   "protoc",
					outputs:      []path.Path{outRel, dirRel + "/"},
					localOutputs: []path.Path{outRel, dirRel + "/"},
				},
			}
			if err := b.outputs(ctx, genStep); err != nil {
				t.Fatalf("outputs()=%v; want nil", err)
			}
			if len(needFlushCalls) != 0 {
				t.Errorf("NeedFlush called for outputs already in LocalOutputs: %v", needFlushCalls)
			}

			// Downstream step depending on outRel must now see errDirty and the
			// updated target state.
			_, _, err = inputMtime(ctx, b, consumerDef)
			if !errors.Is(err, errDirty) {
				t.Errorf("inputMtime() after outputs err=%v; want %v", err, errDirty)
			}
			ts, ok := b.targets.Load(string(outRel))
			if !ok {
				t.Fatalf("b.targets.Load(%q) not found", outRel)
			}
			if !ts.changed || !ts.mtime.Equal(newTime) || ts.dirtyErr != nil {
				t.Errorf("b.targets.Load(%q)=%+v; want mtime=%v changed=true dirtyErr=nil", outRel, ts, newTime)
			}
			dirTS, ok := b.targets.Load(string(dirRel))
			if !ok {
				t.Fatalf("b.targets.Load(%q) not found", dirRel)
			}
			if !dirTS.changed || !dirTS.mtime.Equal(dirMemberTime) || dirTS.dirtyErr != nil {
				t.Errorf("b.targets.Load(%q)=%+v; want effective mtime=%v changed=true dirtyErr=nil", dirRel, dirTS, dirMemberTime)
			}

			// Verify both outputs were flushed to local disk.
			if got, err := os.ReadFile(fullOut); err != nil || !bytes.Equal(got, newContent) {
				t.Errorf("ReadFile(%q) = %q, %v; want %q, nil", fullOut, got, err, newContent)
			}
			fullDirMember := filepath.Join(env.dir, string(dirMemberRel))
			if got, err := os.ReadFile(fullDirMember); err != nil || !bytes.Equal(got, newContent) {
				t.Errorf("ReadFile(%q) = %q, %v; want %q, nil", fullDirMember, got, err, newContent)
			}
		})
	}
}

// TestOutputs_OutputLocal_MissingRequiredOutput verifies that (*Builder).outputs
// still checks for missing required outputs and records dirtyErr in b.targets
// when step.def.LocalOutputs includes those outputs (b/590907111).
func TestOutputs_OutputLocal_MissingRequiredOutput(t *testing.T) {
	ctx := t.Context()
	env := newRacingTestEnv(t, &reapitest.Fake{})
	b := env.newBuilder(t, nil)

	outRel := path.Path("out/siso/gen/missing.h")
	b.targets.Store(string(outRel), targetState{
		mtime:   time.Now().Add(-1 * time.Hour),
		changed: false,
	})

	cmd := &execute.Cmd{
		HashFS:        env.hashFS,
		WorkspaceRoot: env.dir,
		Outputs:       []path.Path{outRel},
	}
	cmd.SetActionResult(&rpb.ActionResult{ExitCode: 0}, false)
	step := &Step{
		outputPaths:    []string{"gen/missing.h"},
		cmd:            cmd,
		state:          &stepState{},
		startReported:  new(sync.Once),
		finishReported: new(sync.Once),
		def: fakeStepDef{
			actionName:   "protoc",
			outputs:      []path.Path{outRel},
			localOutputs: []path.Path{outRel},
		},
	}

	err := b.outputs(ctx, step)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("outputs()=%v; want wrapping %v", err, fs.ErrNotExist)
	}
	ts, ok := b.targets.Load(string(outRel))
	if !ok {
		t.Fatalf("b.targets.Load(%q) not found", outRel)
	}
	if !errors.Is(ts.dirtyErr, fs.ErrNotExist) {
		t.Errorf("b.targets.Load(%q).dirtyErr=%v; want %v (full state: %+v)", outRel, ts.dirtyErr, fs.ErrNotExist, ts)
	}
}
