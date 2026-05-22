// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"sort"
	"sync"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"
)

// AccessRecorder collects the set of input file paths opened during a single
// action execution. It is safe for concurrent use.
type AccessRecorder struct {
	mu    sync.Mutex
	paths map[string]struct{}
}

// NewAccessRecorder creates a new AccessRecorder.
func NewAccessRecorder() *AccessRecorder {
	return &AccessRecorder{paths: make(map[string]struct{})}
}

// Record adds an input path to the set of observed inputs.
func (r *AccessRecorder) Record(inputPath string) {
	r.mu.Lock()
	r.paths[inputPath] = struct{}{}
	r.mu.Unlock()
}

// ObservedInputs returns a sorted, deduplicated list of all input paths
// that were opened during the action execution.
func (r *AccessRecorder) ObservedInputs() []string {
	r.mu.Lock()
	result := make([]string, 0, len(r.paths))
	for p := range r.paths {
		result = append(result, p)
	}
	r.mu.Unlock()
	sort.Strings(result)
	return result
}

// attachObservedInputs attaches the list of observed input paths to the
// action result as auxiliary metadata. The data is stored as a
// google.protobuf.Struct in ExecutedActionMetadata.auxiliary_metadata,
// which survives action cache lookups.
func attachObservedInputs(ar *repb.ActionResult, recorder *AccessRecorder) error {
	inputs := recorder.ObservedInputs()
	if len(inputs) == 0 {
		return nil
	}

	values := make([]*structpb.Value, len(inputs))
	for i, p := range inputs {
		values[i] = structpb.NewStringValue(p)
	}
	s := &structpb.Struct{
		Fields: map[string]*structpb.Value{
			"observed_inputs": structpb.NewListValue(&structpb.ListValue{Values: values}),
		},
	}

	a, err := anypb.New(s)
	if err != nil {
		return err
	}

	if ar.ExecutionMetadata == nil {
		ar.ExecutionMetadata = &repb.ExecutedActionMetadata{}
	}
	ar.ExecutionMetadata.AuxiliaryMetadata = append(ar.ExecutionMetadata.AuxiliaryMetadata, a)
	return nil
}
