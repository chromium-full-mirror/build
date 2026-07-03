// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	log "github.com/golang/glog"

	rpb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/reapi/merkletree"
)

type twoPhaseCaching interface {
	// ComputeLookupKey computes two phase cache lookup key for the step.
	ComputeLookupKey(ctx context.Context, step *Step) (string, error)

	// Check checks two phase caching for the step using lookupKey.
	// return nil when cache hit and populated outputs.
	// return non-nil error otherwise.
	Check(ctx context.Context, lookupKey string, step *Step) error

	// Add adds two phase caching for the step with lookupKey.
	Add(ctx context.Context, lookupKey string, step *Step) error
}

func (b *Builder) twoPhaseCachingLookup(ctx context.Context, step *Step) error {
	ctx, span := trace.NewSpan(ctx, "twophasecaching-lookup")
	defer span.Close(nil)
	// disable canonicalize dir to match input root with local
	// TODO: canonicalize dir in matchInputRoot.
	step.cmd.CanonicalizeDir = false

	err := b.twoPhaseCachingSema.Do(ctx, func(ctx context.Context) error {
		lookupKey, err := b.twoPhaseCaching.ComputeLookupKey(ctx, step)
		if err != nil {
			return err
		}
		step.metrics.TwoPhaseCachingKey = lookupKey
		return b.twoPhaseCaching.Check(ctx, lookupKey, step)
	})
	return err
}

type reapiTwoPhaseCaching struct {
	b              *Builder
	actionCacheMap actionCacheMap
}

func (reapiTwoPhaseCaching) ComputeLookupKey(ctx context.Context, step *Step) (string, error) {
	pcmd := step.cmd.Clone()
	pcmd.Inputs = step.def.TriggerInputs(ctx)
	pcmd.Pure = true // not pure, but to make calculate digest.
	d, err := pcmd.Digest(ctx, nil)
	if err != nil {
		return "", err
	}
	return d.String(), nil
}

func (rt reapiTwoPhaseCaching) Check(ctx context.Context, lookupKey string, step *Step) error {
	if log.V(1) {
		clog.Infof(ctx, "two phase cache: lookup=%s", lookupKey)
	}
	ocmd := step.cmd
	started := time.Now()
	step.metrics.CacheStartTime = IntervalMetric(started.Sub(rt.b.start))
	defer func() {
		step.metrics.CacheTime = IntervalMetric(time.Since(started))
	}()

	nactions := 0
	for action, err := range rt.actionCacheMap.List(ctx, lookupKey) {
		nactions++
		if err != nil {
			step.cmd = ocmd
			return fmt.Errorf("list actions: %w", err)
		}
		inputs, outputs, err := rt.matchAction(ctx, step, action)
		if err != nil {
			clog.Infof(ctx, "mismatch action %s: %v", action, err)
			continue
		}
		if log.V(1) {
			actionDigest, err := digest.FromProtoMessage(action)
			clog.Infof(ctx, "match action %s => %s: %v", action, actionDigest, err)
		}
		step.cmd = ocmd.Clone()
		// use the same inputs, outputs as action.
		step.cmd.Inputs = path.Paths(inputs)
		step.cmd.Outputs = path.Paths(outputs)
		if step.cmd.Depfile != "" {
			// but need to delete depfile from outputs.
			// depfile is added in AllOutputs.
			step.cmd.Outputs = slices.DeleteFunc(step.cmd.Outputs, func(s path.Path) bool {
				return s == step.cmd.Depfile
			})
		}
		if log.V(2) {
			clog.Infof(ctx, "inputs %q", step.cmd.Inputs)
			clog.Infof(ctx, "outputs %q", step.cmd.Outputs)
		}
		step.cmd.Pure = true
		err = rt.b.execRemoteCache(ctx, step)
		if err != nil {
			// cache miss
			clog.Warningf(ctx, "cache miss: %v", err)
			step.cmd = ocmd
			continue
		}
		// cache hit
		if log.V(1) {
			clog.Infof(ctx, "cache hit in two phase caching")
		}
		step.metrics.TwoPhaseCachingActions = nactions
		return nil
	}
	step.cmd = ocmd
	step.metrics.TwoPhaseCachingActions = nactions
	return fmt.Errorf("cache miss %d for %s", nactions, lookupKey)
}

func (rt reapiTwoPhaseCaching) matchAction(ctx context.Context, step *Step, action *rpb.Action) (inputs, outputs []string, retErr error) {
	ctx, span := trace.NewSpan(ctx, "twophasecaching-match-action")
	defer span.Close(nil)
	// TODO: check by digest, and fetch only if digest mismatch?
	// if match digest, outputs should be the same in action.
	cmdDigest := digest.FromProto(action.GetCommandDigest())
	cmd := &rpb.Command{}
	err := rt.b.reapiclient.Proto(ctx, cmdDigest, cmd)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch command for %s: %w", cmdDigest, err)
	}
	// check command line
	if !slices.Equal(step.cmd.Args, cmd.GetArguments()) {
		return nil, nil, fmt.Errorf("arguments mismatch with %s", cmdDigest)
	}
	if string(step.cmd.WorkDir) != cmd.GetWorkingDirectory() {
		return nil, nil, fmt.Errorf("working_dir mismatch %q != %q", step.cmd.WorkDir, cmd.GetWorkingDirectory())
	}
	// TODO: check environment variables

	for _, output := range cmd.GetOutputFiles() { //nolint:staticcheck // existing deprecation
		outputs = append(outputs, rt.b.path.MaybeFromRelative(ctx, output))
	}
	for _, output := range cmd.GetOutputPaths() {
		outputs = append(outputs, rt.b.path.MaybeFromRelative(ctx, output))
	}

	// check input root
	inputRootDigest := digest.FromProto(action.GetInputRootDigest())
	inputs, err = rt.matchInputRoot(ctx, inputRootDigest)
	if err != nil {
		return nil, nil, fmt.Errorf("input_root mismatch with %s: %w", inputRootDigest, err)
	}
	return inputs, outputs, nil
}

func (rt reapiTwoPhaseCaching) matchInputRoot(ctx context.Context, inputRootDigest digest.Digest) ([]string, error) {
	ctx, span := trace.NewSpan(ctx, "twophasecaching-match-input-root")
	defer span.Close(nil)
	var inputs []string
	err := rt.b.reapiclient.WalkDir(ctx, inputRootDigest, func(dname string, dir *rpb.Directory) error {
		if log.V(2) {
			clog.Infof(ctx, "walkdir dir %q: %v", dname, dir)
		}
		m := make(map[string]merkletree.Entry)
		var names []string
		for _, file := range dir.Files {
			name := filepath.ToSlash(filepath.Join(dname, file.Name))
			names = append(names, name)
			m[name] = merkletree.Entry{
				Name:         path.Path(name),
				Data:         digest.NewData(nil, digest.FromProto(file.Digest)),
				IsExecutable: file.IsExecutable,
			}
		}
		for _, dir := range dir.Directories {
			name := filepath.ToSlash(filepath.Join(dname, dir.Name))
			names = append(names, name)
			m[name] = merkletree.Entry{
				Name: path.Path(name),
			}
		}
		for _, symlink := range dir.Symlinks {
			name := filepath.ToSlash(filepath.Join(dname, symlink.Name))
			names = append(names, name)
			m[name] = merkletree.Entry{
				Name:   path.Path(name),
				Target: symlink.Target,
			}
		}
		if log.V(2) {
			clog.Infof(ctx, "walkdir check %q", names)
		}
		// entries from workspace root to get symlink correctly.
		ents, err := rt.b.hashFS.Entries(ctx, rt.b.path.WorkspaceRoot, path.Paths(names))
		if err != nil {
			return fmt.Errorf("entries: %w", err)
		}
		if log.V(2) {
			clog.Infof(ctx, "walkdir entries %v", len(ents))
		}
		if len(ents) != len(names) {
			return fmt.Errorf("missing some entries locally (local:%d, expected:%d)", len(ents), len(names))
		}
		for _, ent := range ents {
			e, ok := m[string(ent.Name)]
			if !ok {
				return fmt.Errorf("missing %s in %s", ent.Name, dname)
			}
			if !e.Data.IsZero() {
				// want file
				if ent.Data.Digest() != e.Data.Digest() {
					return fmt.Errorf("mismatch %s in %s: digest local:%s != want:%s", ent.Name, dname, ent.Data.Digest(), e.Data.Digest())
				}
				if ent.IsExecutable != e.IsExecutable {
					return fmt.Errorf("mismatch %s in %s: is_executable local:%t != want:%t", ent.Name, dname, ent.IsExecutable, e.IsExecutable)
				}
				continue
			}
			if e.Target != "" {
				// want symlink
				if ent.Target != e.Target {
					return fmt.Errorf("mismatch %s in %s: target local:%q != want:%q", ent.Name, dname, ent.Target, e.Target)
				}
				continue
			}
			// want dir
			if !ent.Data.IsZero() {
				return fmt.Errorf("mismatch %s in %s: local digest:%s want:dir", ent.Name, dname, ent.Data.Digest())
			}
			if ent.Target != "" {
				return fmt.Errorf("mismatch %s in %s: local symlink:%q want:dir", ent.Name, dname, ent.Target)
			}
		}
		if log.V(2) {
			clog.Infof(ctx, "walkdir dir %q: match", dname)
		}
		inputs = append(inputs, names...)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return inputs, nil
}

func (rt reapiTwoPhaseCaching) Add(ctx context.Context, lookupKey string, step *Step) error {
	ctx, span := trace.NewSpan(ctx, "twophasecaching-add-action-in-cache")
	defer span.Close(nil)
	err := rt.b.cacheWrite(ctx, step)
	if errors.Is(err, errNoCacheWrite) {
		clog.Infof(ctx, "two phase cache: write ignored %s: %v", lookupKey, err)
		return err
	} else if err != nil {
		clog.Warningf(ctx, "two phase cache: write failed %s: %v", lookupKey, err)
		return err
	}
	// associate action to lookupKey.
	clog.Infof(ctx, "two phase cache: add lookup=%s action=%s", lookupKey, step.cmd.ActionDigest())
	return rt.actionCacheMap.Add(ctx, lookupKey, step.cmd.ActionDigest())
}
