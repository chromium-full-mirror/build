// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.chromium.org/build/siso/o11y/trace"
	"go.chromium.org/build/siso/path"
)

func preprocCmd(ctx context.Context, b *Builder, step *Step) error {
	ensureExpanded(ctx, b, step)
	step.setPhase(stepPreproc)
	err := b.preprocSema.Do(ctx, step.weight, func(ctx context.Context) error {
		ctx, span := trace.NewSpan(ctx, spanPreproc)
		defer span.Close(nil)
		err := depsCmd(ctx, b, step)
		if err != nil {
			// disable remote execution. b/289143861
			step.cmd.Platform = nil
			return fmt.Errorf("disable remote: failed to get %s deps: %w", step.cmd.Deps, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	dedupInputs(ctx, step.cmd)
	return nil
}

func uniqueFiles(inputsList ...[]string) []string {
	seen := make(map[string]bool)
	var inputs []string
	for _, ins := range inputsList {
		for _, in := range ins {
			if in == "" {
				continue
			}
			if seen[in] {
				continue
			}
			seen[in] = true
			inputs = append(inputs, in)
		}
	}
	r := make([]string, len(inputs))
	copy(r, inputs)
	return r
}

func uniquePathFiles(inputsList ...[]path.Path) []path.Path {
	// Most deps inputs are unique, so size for all.
	n := 0
	for _, ins := range inputsList {
		n += len(ins)
	}
	seen := make(map[path.Path]bool, n)
	inputs := make([]path.Path, 0, n)
	for _, ins := range inputsList {
		for _, in := range ins {
			if in == "" {
				continue
			}
			if seen[in] {
				continue
			}
			seen[in] = true
			inputs = append(inputs, in)
		}
	}
	if cap(inputs) > 2*len(inputs) {
		// many duplicates: don't keep the oversized array alive.
		return slices.Clone(inputs)
	}
	return slices.Clip(inputs)
}

func (b *Builder) expandInputs(ctx context.Context, inputs []string) []string {
	m := b.graph.InputDeps(ctx)
	v := make(map[string]bool)
	for len(inputs) > 0 {
		f := inputs[0]
		inputs = inputs[1:]
		if v[f] {
			continue
		}
		v[f] = true
		deps, ok := m[f]
		if ok {
			inputs = append(inputs, deps...)
		}
	}
	inputs = inputs[:0]
	for k := range v {
		if strings.Contains(k, ":") {
			continue
		}
		inputs = append(inputs, k)
	}
	sort.Strings(inputs)
	return inputs
}
