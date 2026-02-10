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
// The builder will record the item in an "unresolved" state.
// Returns a list of labels of dependencies the item needs but the builder hasn't seen yet,
// along with the location where the dep was defined.
// Returns an error if there was an issue updating the builder's records.
//
// Callers of this function are responsible for loading the buildfile(s) containing the deps
// requested.
func (b *Builder) RecordDefinedItem(item Item) ([]environment.LabelWithOrigin, error) {
	// If there were items waiting for this one to be defined, a record already exists.
	// Try to get the existing record, else create a new record.
	label := item.Label()
	record, ok := b.records[label]
	if ok {
		// Check types, if the record was not just created.
		if !record.item.compatibleWith(item) {
			return nil, ItemTypeMismatchError{
				OriginNode:        parse.OriginNode{Node: item.DefinedFrom()},
				label:             label,
				itemOrPlaceholder: item,
				existingRecord:    record,
			}
		}
		// Check that it's not been already defined.
		if record.state != itemStateUndefined {
			return nil, ItemRedefinedError{
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
	case *Target:
		return b.targetDefined(i, record)
	case *Config:
		// HACK: Temporarily do not throw NotImplementedError for test to work.
		fmt.Fprintf(os.Stderr, "got config %q but will do nothing yet! need to parse this config's deps.\n",
			record.item.Label().UserVisibleString(false))
		return nil, nil
	case *Toolchain:
		if b.seenDefaultToolchain {
			return nil, NotImplementedError{
				what: "Support for multiple toolchains is not implemented yet.",
			}
		}
		// Don't need to do anything for first toolchain yet, Loader has already seen it.
		// Also don't support parsing pool(), deps, etc yet so nothing to do right now.
		b.seenDefaultToolchain = true
		return nil, nil
	}
	return nil, fmt.Errorf("don't know how to handle %T item yet", item)
}

func (b *Builder) targetDefined(target *Target, record *builderRecord) ([]environment.LabelWithOrigin, error) {
	var unresolvedDeps []environment.LabelWithOrigin

	// Find all variables in this target that references labels.
	for _, varName := range slices.Sorted(maps.Keys(target.schema.vars)) {
		varType := target.schema.vars[varName]
		// Determine the type of label expected.
		// TODO: only supports lists of labels right now, need to support single labels too?
		var expectedPlaceholder Item
		switch varType {
		case targetLabelListType:
			expectedPlaceholder = &Target{}
		case configLabelListType:
			expectedPlaceholder = &Config{}
		default:
			continue
		}

		// Get the list.
		// TODO: only supports lists of labels right now, need to support single labels too?
		val, ok := target.values[varName]
		if !ok {
			continue
		}
		listValue, err := processedValueAs[labelListValue](val)
		if err != nil {
			return nil, err
		}

		// For each label, ensure the record exists.
		// Collect deps that aren't yet resolved.
		for _, dep := range listValue.list {
			depRecord, err := b.recordFor(dep.Label, dep.Origin, expectedPlaceholder)
			if err != nil {
				return nil, err
			}
			if depRecord.state != itemStateResolved {
				unresolvedDeps = append(unresolvedDeps, dep)
			}
			record.addDep(depRecord)
		}
	}

	// If this target's toolchain hasn't been resolved, add it to the requested deps.
	toolchainDep := environment.LabelWithOrigin{
		Label:  target.settings.toolchainLabel,
		Origin: target.definedFrom,
	}
	toolchainRec, err := b.recordFor(toolchainDep.Label, toolchainDep.Origin, &Toolchain{})
	if err != nil {
		return nil, err
	}
	if toolchainRec.state != itemStateResolved {
		unresolvedDeps = append(unresolvedDeps, toolchainDep)
	}
	record.addDep(toolchainRec)

	if len(unresolvedDeps) == 0 {
		return nil, b.resolveTarget(target, record)
	}
	return unresolvedDeps, nil
}

func (b *Builder) resolveTarget(target *Target, record *builderRecord) error {
	if target.schema == nil {
		return environment.IllegalStateError{
			Reason: "Attempted to resolve target without schema",
		}
	}
	if target.schema.resolver == nil {
		fmt.Fprintf(os.Stderr, "ignoring target %v for now since no resolver...\n", target.label.UserVisibleString(false))
		return nil
	}
	outFile, err := target.schema.resolver(resolverContext{
		declareTool:    target.declareTool,
		stringFor:      target.stringFor,
		sourceFilesFor: target.sourceFilesFor,
		resolvedTargetsFor: func(varName string) iter.Seq2[resolution, error] {
			return func(yield func(resolution, error) bool) {
				deps, err := target.labelsFor(varName)
				if err != nil {
					yield(resolution{}, err)
					return
				}
				for _, dep := range deps {
					depRecord, err := b.recordFor(dep.Label, dep.Origin, &Target{})
					if err != nil {
						yield(resolution{}, err)
						return
					}
					if depRecord.state != itemStateResolved {
						yield(resolution{}, environment.IllegalStateError{Reason: "unresolved dep found"})
						return
					}
					switch t := depRecord.item.(type) {
					case *Target:
						if !yield(t.resolution, nil) {
							return
						}
					default:
						yield(resolution{}, ItemTypeMismatchError{
							OriginNode:        parse.OriginNode{Node: dep.Origin},
							label:             t.Label(),
							itemOrPlaceholder: &Target{},
							existingRecord:    depRecord,
						})
						return
					}
				}
			}
		},
	})
	if err != nil {
		return err
	}
	target.resolution.output = outFile
	record.state = itemStateResolved

	// Recursively update everybody waiting on this item to be resolved.
	for dependent := range record.dependents {
		dependent.unresolvedDeps--
		if dependent.unresolvedDeps > 0 {
			continue
		}
		switch dependentTarget := dependent.item.(type) {
		case *Target:
			if err := b.resolveTarget(dependentTarget, dependent); err != nil {
				return err
			}
		default:
			return environment.IllegalStateError{
				Reason: "Builder constructed graph with non-target dep on target",
			}
		}
	}
	return nil
}

// recordFor returns the record associated with the given label. Checks
// that if we already have references for it, the type matches. If no record
// exists yet, a new one will be created.
//
// If any of the conditions fail, the return value will be nil and the error
// will be set. requestFrom is used as the source of the error.
func (b *Builder) recordFor(label environment.Label, requestFrom parse.Node, itemOrPlaceholder Item) (*builderRecord, error) {
	if record, ok := b.records[label]; ok {
		// Check types, if the record was not just created.
		if !record.item.compatibleWith(itemOrPlaceholder) {
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
