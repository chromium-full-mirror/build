// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package analysis loads and analyzes GN buildfiles to produce the resolved dependency graph.
package analysis

import (
	"fmt"
	"os"

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
// NOT IMPLEMENTED YET: If there are no unresolved deps, the builder will attempt to move this
// item into a resolved state, and recursively attempt to move any dependents that were waiting
// for this item to a resolved state as well.
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
		fmt.Fprintf(os.Stderr, "got target %q but placeholder! need to parse this target's configs, non-private deps etc.\n",
			record.item.Label().UserVisibleString(false))
		return b.targetDefined(i, record)
	case *Config:
		fmt.Fprintf(os.Stderr, "got config %q but will do nothing yet! need to parse this config's deps.\n",
			record.item.Label().UserVisibleString(false))
	case *Toolchain:
		fmt.Fprintf(os.Stderr, "got toolchain %q but will do nothing yet! need to parse this toolchain's deps and tool() defs.\n",
			record.item.Label().UserVisibleString(false))
	}

	// TODO: return to caller the dependencies that need to be load
	// before this item will be resolved.

	return nil, nil
}

func (b *Builder) targetDefined(target *Target, record *builderRecord) ([]environment.LabelWithOrigin, error) {
	var unresolvedDeps []environment.LabelWithOrigin

	// Iterate all deps.
	for _, dep := range target.privateDeps {
		// We might've seen the dep itself or another target request the same dep.
		// Try to get the existing record, else create a new record.
		depRecord, ok := b.records[dep.Label]
		if ok {
			// Ensure dep is a target, if the record was already created.
			if !depRecord.item.compatibleWith(&Target{}) {
				return nil, ItemTypeMismatchError{
					OriginNode:        parse.OriginNode{Node: dep.Origin},
					label:             dep.Label,
					itemOrPlaceholder: &Target{},
					existingRecord:    depRecord,
				}
			}
		} else {
			b.records[dep.Label] = newBuilderRecord(&Target{}, dep.Origin)
			depRecord = b.records[dep.Label]
		}
		record.addDep(depRecord)
		if depRecord.state != itemStateResolved {
			unresolvedDeps = append(unresolvedDeps, environment.LabelWithOrigin{
				Label:  dep.Label,
				Origin: dep.Origin,
			})
		}
	}

	return unresolvedDeps, nil
}
