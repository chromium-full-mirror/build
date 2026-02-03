// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import "go.chromium.org/build/gong/gn/parse"

type itemState int

const (
	itemStateUndefined itemState = iota
	itemStateUnresolved
	itemStateResolved
)

// builderRecord is used by the builder to manage the loading of the dependency
// tree. It holds a reference to an item and links to other records that the
// item depends on, both resolved ones, and unresolved ones.
//
// If a target depends on another one that hasn't been defined yet, we'll make
// a placeholder builderRecord with the zero value for the item.
// The type of the zero value item can be used once the item's definition is
// seen to check whether the expected type matches.
//
// The item will get filled in when we encounter the declaration for the item
// (or when we're done and realize there are undefined items).
type builderRecord struct {
	item  Item
	state itemState
	// The node that referenced this item.
	referencedFrom parse.Node
	// Dependencies from this item, either resolved or unresolved.
	dependencies map[*builderRecord]struct{}
	dependents   map[*builderRecord]struct{}
	// How many dependencies are not resolved yet.
	// Keep track here instead of checking each dep, so verifying all deps resolved is O(1).
	// When deps are resolved, the builder needs to decrement this num in each dependent.
	unresolvedDeps int
}

// newBuilderRecord creates a new builder record.
//
// The item can be the zero value for a concrete item, see the [builderRecord] docs
// for how this is used.
func newBuilderRecord(item Item, referencedFrom parse.Node) *builderRecord {
	return &builderRecord{
		item:           item,
		state:          itemStateUndefined,
		referencedFrom: referencedFrom,
		dependencies:   make(map[*builderRecord]struct{}),
		dependents:     make(map[*builderRecord]struct{}),
	}
}

// addDep adds the dep if it hasn't been seen, incrementing the unresolved deps count if needed.
func (r *builderRecord) addDep(dep *builderRecord) {
	if _, ok := r.dependencies[dep]; !ok {
		r.dependencies[dep] = struct{}{}
		if dep.state != itemStateResolved {
			r.unresolvedDeps++
		}
		dep.dependents[r] = struct{}{}
	}
}
