// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package analysis loads and analyzes GN buildfiles to produce the resolved dependency graph.
package analysis

import (
	"fmt"
	"iter"
	"maps"
	"os"
	"slices"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/parse"
)

// Builder assembles the GN dependency graph.
//
// The builder is responsible for receiving all item (target, config, toolchain, pool) definitions,
// and requesting loads of their dependencies where necessary.
//
// Item records will start in an unresolved state.
type Builder struct {
	loader *Loader
	// All items, resolved or unresolved, are tracked here.
	records map[environment.Label]*builderRecord
	// Temporarily only support one toolchain.
	seenDefaultToolchain bool
}

// MakeBuilder constructs a new builder.
func MakeBuilder(loader *Loader) Builder {
	return Builder{
		loader:  loader,
		records: make(map[environment.Label]*builderRecord),
	}
}

// RecordDefinedItem receives an item definition, normally created by loading buildfiles.
//
// Returns ONE of the following:
//   - If the item has newly-discovered dependencies that the builder hasn't seen yet,
//     a slice of labels to these deps (and the origin of each dep for error-reporting
//     purposes), OR
//   - If the item's dependencies are fully resolved, a slice of item(s) that were
//     successfully resolved (this allows callers to eagerly collect resolved items,
//     rather than waiting til the build graph is fully resolved), including the item
//     itself, OR
//   - An error if there was an issue updating the builder's records.
//
// Callers of this function are responsible for loading the buildfile(s) containing the deps
// requested.
func (b *Builder) RecordDefinedItem(item graph.Item) ([]environment.LabelWithOrigin, []graph.Item, error) {
	// If there were items waiting for this one to be defined, a record already exists.
	// Try to get the existing record, else create a new record.
	label := item.Label()
	record, ok := b.records[label]
	if ok {
		// Check types, if the record was not just created.
		if !record.item.CompatibleWith(item) {
			return nil, nil, ItemTypeMismatchError{
				OriginNode:        parse.OriginNode{Node: item.DefinedFrom()},
				label:             label,
				itemOrPlaceholder: item,
				existingRecord:    record,
			}
		}
		// Check that it's not been already defined.
		if record.state != itemStateUndefined {
			return nil, nil, ItemRedefinedError{
				previousOrigin: record.item.DefinedFrom(),
				duplicateItem:  item,
			}
		}
		// Verified undefined item of same type, therefore safe to record.
		record.item = item
	} else {
		// No record yet, need to create with this item.
		b.records[label] = newBuilderRecord(item, item.DefinedFrom())
		record = b.records[label]
	}

	// Obtained a record in undefined state, now ready to move into unresolved state.
	record.state = itemStateUnresolved

	// Do target-specific dependency setup.
	switch i := item.(type) {
	case *graph.Target:
		return b.targetDefined(i, record)
	case *graph.Config:
		// HACK: Temporarily do not throw NotImplementedError for test to work.
		fmt.Fprintf(os.Stderr, "got config %q but will do nothing yet! need to parse this config's deps.\n",
			record.item.Label().UserVisibleString(false))
		return nil, nil, nil
	case *graph.Toolchain:
		return b.toolchainDefined(i, record)
	}
	return nil, nil, fmt.Errorf("don't know how to handle %T item yet", item)
}

func (b *Builder) targetDefined(target *graph.Target, record *builderRecord) ([]environment.LabelWithOrigin, []graph.Item, error) {
	var unresolvedDeps []environment.LabelWithOrigin

	// Find all variables in this target that references labels.
	for _, varName := range slices.Sorted(maps.Keys(target.Schema.Vars)) {
		varType := target.Schema.Vars[varName]
		expectedPlaceholder := varType.ExpectedItems()
		if expectedPlaceholder == nil {
			continue
		}
		val, ok := target.Values[varName]
		if !ok {
			continue
		}
		for dep := range val.Labels() {
			depRecord, err := b.recordFor(dep.Label, dep.Origin, expectedPlaceholder)
			if err != nil {
				return nil, nil, err
			}
			if depRecord.state != itemStateResolved {
				unresolvedDeps = append(unresolvedDeps, dep)
			}
			record.addDep(depRecord)
		}
	}

	// If this target's toolchain hasn't been resolved, add it to the requested deps.
	toolchainDep := environment.LabelWithOrigin{
		Label:  target.Label().ToolchainLabel(),
		Origin: target.DefinedFrom(),
	}
	toolchainRec, err := b.recordFor(toolchainDep.Label, toolchainDep.Origin, &graph.Toolchain{})
	if err != nil {
		return nil, nil, err
	}
	if toolchainRec.state != itemStateResolved {
		unresolvedDeps = append(unresolvedDeps, toolchainDep)
	}
	record.addDep(toolchainRec)

	// Return with the unresolved deps if we have any.
	if record.unresolvedDeps > 0 {
		return unresolvedDeps, nil, nil
	}

	// Otherwise we can immediately try to resolve this target.
	allResolved, err := b.resolveTarget(target, record)
	if err != nil {
		return nil, nil, err
	}
	return nil, allResolved, nil
}

// TODO: Support more than one toolchain.
func (b *Builder) toolchainDefined(toolchain *graph.Toolchain, record *builderRecord) ([]environment.LabelWithOrigin, []graph.Item, error) {
	if b.seenDefaultToolchain {
		return nil, nil, NotImplementedError{
			what: "Support for multiple toolchains is not implemented yet.",
		}
	}

	// Don't need to do anything for first toolchain yet, Loader has already seen it.
	// Also don't support parsing pool(), deps, etc yet so nothing to do right now.
	b.seenDefaultToolchain = true
	record.state = itemStateResolved

	// Recursively update everybody waiting on this item to be resolved.
	allResolved := []graph.Item{toolchain}
	for dependent := range record.dependents {
		dependent.unresolvedDeps--
		if dependent.unresolvedDeps > 0 {
			continue
		}
		switch dependentItem := dependent.item.(type) {
		case *graph.Target:
			resolved, err := b.resolveTarget(dependentItem, dependent)
			if err != nil {
				return nil, nil, err
			}
			allResolved = append(allResolved, resolved...)
		default:
			fmt.Fprintf(os.Stderr, "don't know how to resolve %T items yet, skipping\n", dependentItem)
		}
	}
	return nil, allResolved, nil
}

// resolveTarget attempts to resolve the target, recursively resolving dependents if found.
// All resolved targets are returned.
func (b *Builder) resolveTarget(target *graph.Target, record *builderRecord) ([]graph.Item, error) {
	if target.Schema == nil {
		return nil, environment.IllegalStateError{
			Reason: "Attempted to resolve target without schema",
		}
	}
	if target.Schema.Resolver == nil {
		fmt.Fprintf(os.Stderr, "ignoring target %v for now since no resolver...\n", target.Label().UserVisibleString(false))
		return nil, nil
	}

	// Determine the outdir for the target.
	// TODO: Placeholder implementation that always assumes obj/.
	// To be correct, we need to also support absolute paths, support gen/, support phony/, etc.
	outDir, err := b.loader.buildSettings.BuildDir.ResolveRelativeDir("obj/" + target.Label().Dir.Path())
	if err != nil {
		return nil, err
	}

	result, err := target.Schema.Resolver(graph.ResolverContext{
		DeclareTool: func(tool string, source fs.SourceFile, inputs []fs.SourceFile, outputName string, expansions map[string]string) (fs.SourceFile, error) {
			return target.DeclareTool(outDir, tool, source, inputs, outputName, expansions)
		},
		LabelKeyedStringMapFor: target.LabelKeyedStringMapFor,
		StringFor:              target.StringFor,
		SourceFileFor:          target.SourceFileFor,
		SourceFilesFor:         target.SourceFilesFor,
		ResolvedTargetsFor: func(varName string) iter.Seq2[graph.Resolution, error] {
			return func(yield func(graph.Resolution, error) bool) {
				deps, err := target.LabelsFor(varName)
				if err != nil {
					yield(graph.Resolution{}, err)
					return
				}
				for _, dep := range deps {
					depRecord, err := b.recordFor(dep.Label, dep.Origin, &graph.Target{})
					if err != nil {
						yield(graph.Resolution{}, err)
						return
					}
					if depRecord.state != itemStateResolved {
						yield(graph.Resolution{}, environment.IllegalStateError{Reason: "unresolved dep found"})
						return
					}
					switch t := depRecord.item.(type) {
					case *graph.Target:
						if !yield(t.Resolution, nil) {
							return
						}
					default:
						yield(graph.Resolution{}, ItemTypeMismatchError{
							OriginNode:        parse.OriginNode{Node: dep.Origin},
							label:             t.Label(),
							itemOrPlaceholder: &graph.Target{},
							existingRecord:    depRecord,
						})
						return
					}
				}
			}
		},
	})
	if err != nil {
		return nil, err
	}
	target.Resolution.Label = target.Label()
	target.Resolution.Metadata = result
	record.state = itemStateResolved

	// Recursively update everybody waiting on this item to be resolved.
	allResolved := []graph.Item{target}
	for dependent := range record.dependents {
		dependent.unresolvedDeps--
		if dependent.unresolvedDeps > 0 {
			continue
		}
		switch dependentTarget := dependent.item.(type) {
		case *graph.Target:
			resolved, err := b.resolveTarget(dependentTarget, dependent)
			if err != nil {
				return nil, err
			}
			allResolved = append(allResolved, resolved...)
		default:
			return nil, environment.IllegalStateError{
				Reason: "Builder constructed graph with non-target dep on target",
			}
		}
	}
	return allResolved, nil
}

// recordFor returns the record associated with the given label. Checks
// that if we already have references for it, the type matches. If no record
// exists yet, a new one will be created.
//
// If any of the conditions fail, the return value will be nil and the error
// will be set. requestFrom is used as the source of the error.
func (b *Builder) recordFor(label environment.Label, requestFrom parse.Node, itemOrPlaceholder graph.Item) (*builderRecord, error) {
	if record, ok := b.records[label]; ok {
		// Check types, if the record was not just created.
		if !record.item.CompatibleWith(itemOrPlaceholder) {
			return nil, ItemTypeMismatchError{
				OriginNode:        parse.OriginNode{Node: requestFrom},
				label:             label,
				itemOrPlaceholder: itemOrPlaceholder,
				existingRecord:    record,
			}
		}
		return record, nil
	}
	b.records[label] = newBuilderRecord(itemOrPlaceholder, requestFrom)
	return b.records[label], nil
}
