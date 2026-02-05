// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package analysis loads and analyzes GN buildfiles to produce the resolved dependency graph.
package analysis

import (
	"fmt"
	"maps"
	"os"
	"slices"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
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
		listValue, err := resolve.AsValue[*resolve.ListValue](val)
		if err != nil {
			return nil, err
		}

		// For each label, ensure the record exists.
		// Collect deps that aren't yet resolved.
		for rawLabel := range listValue.Values() {
			dep, err := environment.ResolveLabel(target.label.Dir, environment.Label{}, rawLabel)
			if err != nil {
				return nil, err
			}
			depRecord, err := b.recordFor(dep, rawLabel.OriginNode(), expectedPlaceholder)
			if err != nil {
				return nil, err
			}
			if depRecord.state != itemStateResolved {
				unresolvedDeps = append(unresolvedDeps, environment.LabelWithOrigin{
					Label:  dep,
					Origin: rawLabel.OriginNode(),
				})
			}
			record.addDep(depRecord)
		}
	}

	return unresolvedDeps, nil
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
