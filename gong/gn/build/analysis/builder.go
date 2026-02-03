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
// The builder will record the item in its internal state.
// Returns an error if there was an issue updating the builder's records.
//
// The item starts in an unresolved state.
// Not yet implemented: The builder will try to move the item into a resolved state if possible.
func (b *Builder) RecordDefinedItem(item Item) error {
	// If there were items waiting for this one to be defined, a record already exists.
	// Try to get the existing record, else create a new record.
	label := item.Label()
	record, ok := b.records[label]
	if ok {
		// Check types, if the record was not just created.
		if !record.item.compatibleWith(item) {
			return ItemTypeMismatchError{
				OriginNode:        parse.OriginNode{Node: item.DefinedFrom()},
				label:             label,
				itemOrPlaceholder: item,
				existingRecord:    record,
			}
		}
		// Check that it's not been already defined.
		if record.state != itemStateUndefined {
			return ItemRedefinedError{
				previousOrigin: record.item.DefinedFrom(),
				duplicateItem:  item,
			}
		}
	} else {
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
		if err := b.targetDefined(i, record); err != nil {
			return err
		}
	case *Config:
		fmt.Fprintf(os.Stderr, "got config %q but will do nothing yet! need to parse this config's deps.\n",
			record.item.Label().UserVisibleString(false))
	case *Toolchain:
		fmt.Fprintf(os.Stderr, "got toolchain %q but will do nothing yet! need to parse this toolchain's deps and tool() defs.\n",
			record.item.Label().UserVisibleString(false))
	}

	// TODO: return to caller the dependencies that need to be load
	// before this item will be resolved.

	return nil
}

func (b *Builder) targetDefined(target *Target, record *builderRecord) error {
	// Iterate all deps.
	// TODO: Return the list of dependencies that are undefined or unresolved, they need to be resolved before this target can be.
	for _, dep := range target.privateDeps {
		// We might've seen the dep itself or another target request the same dep.
		// Try to get the existing record, else create a new record.
		depRecord, ok := b.records[dep.Label]
		if ok {
			// Ensure dep is a target, if the record was already created.
			if !depRecord.item.compatibleWith(&Target{}) {
				return ItemTypeMismatchError{
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
	}
	return nil
}
