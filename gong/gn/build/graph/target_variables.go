// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"fmt"
	"iter"
	"slices"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

// A TargetVar is the typedef of a variable that a GN target accepts,
// prior to being processed to its internal representation.
type TargetVar interface {
	// ExpectedItems returns a placeholder for the type of item this variable references,
	// if applicable. Otherwise, nil.
	ExpectedItems() Item
	// Process takes a raw buildfile declaration and processes it into the internal representation.
	Process(environment.Label, resolve.Value) (ProcessedValue, error)
}

// A BoolVar variable accepts a boolean e.g.
//
//	testonly = true
type BoolVar struct{}

func (BoolVar) ExpectedItems() Item { return nil }
func (BoolVar) Process(_ environment.Label, value resolve.Value) (ProcessedValue, error) {
	bv, err := resolve.AsValue[*resolve.BooleanValue](value)
	if err != nil {
		return nil, err
	}
	return BoolValue{
		origin: bv,
		bool:   bv.Value(),
	}, nil
}

// A StringVar variable accepts single strings e.g.
//
//	depfile = "$target_gen_dir/$target_name.d"
type StringVar struct{}

func (StringVar) ExpectedItems() Item { return nil }
func (StringVar) Process(_ environment.Label, value resolve.Value) (ProcessedValue, error) {
	sv, err := resolve.AsValue[*resolve.StringValue](value)
	if err != nil {
		return nil, err
	}
	return StringValue{
		origin: sv,
		str:    sv.RawGNString(),
	}, nil
}

// A StringListVar variable accepts lists of strings e.g.
//
//	cflags = [ "-fvisibility=default" ]
type StringListVar struct{}

func (StringListVar) ExpectedItems() Item { return nil }
func (StringListVar) Process(_ environment.Label, value resolve.Value) (ProcessedValue, error) {
	lv, err := resolve.AsValue[*resolve.ListValue](value)
	if err != nil {
		return nil, err
	}
	var list []string
	for v := range lv.Values() {
		sv, err := resolve.AsValue[*resolve.StringValue](v)
		if err != nil {
			return nil, err
		}
		list = append(list, sv.RawGNString())
	}
	return StringListValue{
		origin: lv,
		list:   list,
	}, nil
}

// A FileVar variable accepts single files e.g.
//
//	script = "domything.py"
type FileVar struct{}

func (FileVar) ExpectedItems() Item { return nil }
func (FileVar) Process(targetLabel environment.Label, value resolve.Value) (ProcessedValue, error) {
	sv, err := resolve.AsValue[*resolve.StringValue](value)
	if err != nil {
		return nil, err
	}
	sourceFile, err := targetLabel.Dir.ResolveRelativeFile(sv.RawGNString())
	if err != nil {
		return nil, err
	}
	return FileValue{
		origin: sv,
		file:   sourceFile,
	}, nil
}

// A FileListVar variable accepts lists of files e.g.
//
//	inputs = [ "helper_library.py" ]
type FileListVar struct{}

func (FileListVar) ExpectedItems() Item { return nil }
func (FileListVar) Process(targetLabel environment.Label, value resolve.Value) (ProcessedValue, error) {
	lv, err := resolve.AsValue[*resolve.ListValue](value)
	if err != nil {
		return nil, err
	}
	var list []fs.SourceFile
	for v := range lv.Values() {
		sv, err := resolve.AsValue[*resolve.StringValue](v)
		if err != nil {
			// TODO: "ERROR Items must be strings (filenames)."
			return nil, err
		}
		sourceFile, err := targetLabel.Dir.ResolveRelativeFile(sv.RawGNString())
		if err != nil {
			return nil, err
		}
		list = append(list, sourceFile)
	}
	return FileListValue{
		origin: lv,
		list:   list,
	}, nil
}

// A LabelListVar accepts lists of labels e.g.
//
//	deps = [ ":foo", "//bar:baz" ]
type LabelListVar struct {
	Expected Item
}

func (l LabelListVar) ExpectedItems() Item { return l.Expected }
func (LabelListVar) Process(targetLabel environment.Label, value resolve.Value) (ProcessedValue, error) {
	lv, err := resolve.AsValue[*resolve.ListValue](value)
	if err != nil {
		return nil, err
	}
	var list []environment.LabelWithOrigin
	for v := range lv.Values() {
		sv, err := resolve.AsValue[*resolve.StringValue](v)
		if err != nil {
			return nil, err
		}
		resolvedLabel, err := environment.ResolveLabel(targetLabel.Dir, targetLabel.ToolchainLabel(), sv)
		if err != nil {
			return nil, err
		}
		list = append(list, environment.LabelWithOrigin{
			Label:  resolvedLabel,
			Origin: sv.OriginNode(),
		})
	}
	return LabelListValue{
		Origin: lv,
		List:   list,
	}, nil
}

// A ScopeOfLabelsVar variable accepts a scope whose values are labels e.g.
//
//	aliased_deps = {
//		bar_renamed = ":bar",
//	}
type ScopeOfLabelsVar struct {
	// Invert specifies whether the value should be processed into a map
	// where the keys are the labels.
	Invert bool
}

func (ScopeOfLabelsVar) ExpectedItems() Item { return nil }
func (s ScopeOfLabelsVar) Process(targetLabel environment.Label, value resolve.Value) (ProcessedValue, error) {
	sv, err := resolve.AsValue[*resolve.ScopeValue](value)
	if err != nil {
		return nil, err
	}
	if !s.Invert {
		// TODO: "ERROR ... not supported."
		return nil, fmt.Errorf("ScopeOfLabelsVar with invert=false is not supported")
	}

	res := LabelKeyedStringMapValue{
		origin: sv,
		data:   make(map[environment.Label]string),
	}

	// For now, only support labels as keys.
	for ident, val := range sv.Values() {
		svVal, err := resolve.AsValue[*resolve.StringValue](val)
		if err != nil {
			return nil, err
		}
		resolvedLabel, err := environment.ResolveLabel(targetLabel.Dir, targetLabel.ToolchainLabel(), svVal)
		if err != nil {
			return nil, err
		}
		res.data[resolvedLabel] = ident
		res.labelWithOrigins = append(res.labelWithOrigins, environment.LabelWithOrigin{
			Label:  resolvedLabel,
			Origin: svVal.OriginNode(),
		})
	}
	return res, nil
}

// A ProcessedValue represents a target's variable, after we've converted values such as labels
// into concrete underlying types.
type ProcessedValue interface {
	// value returns the origin AST value.
	value() resolve.Value
	// Labels returns an iterator over all label(s) this value contains, if any.
	Labels() iter.Seq[environment.LabelWithOrigin]
}

// BoolValue represents a processed boolean value.
type BoolValue struct {
	origin *resolve.BooleanValue
	bool   bool
}

func (f BoolValue) value() resolve.Value {
	return f.origin
}
func (BoolValue) Labels() iter.Seq[environment.LabelWithOrigin] { return nil }

// StringValue represents a processed string value.
type StringValue struct {
	origin *resolve.StringValue
	str    string
}

func (f StringValue) value() resolve.Value {
	return f.origin
}
func (StringValue) Labels() iter.Seq[environment.LabelWithOrigin] { return nil }

// StringListValue represents a processed string list value.
type StringListValue struct {
	origin *resolve.ListValue
	list   []string
}

func (f StringListValue) value() resolve.Value {
	return f.origin
}
func (StringListValue) Labels() iter.Seq[environment.LabelWithOrigin] { return nil }

// LabelListValue represents a processed label list value.
type LabelListValue struct {
	Origin *resolve.ListValue
	List   []environment.LabelWithOrigin
}

func (f LabelListValue) value() resolve.Value {
	return f.Origin
}
func (l LabelListValue) Labels() iter.Seq[environment.LabelWithOrigin] {
	return slices.Values(l.List)
}

// FileValue represents a processed file value.
type FileValue struct {
	origin *resolve.StringValue
	file   fs.SourceFile
}

func (f FileValue) value() resolve.Value {
	return f.origin
}
func (FileValue) Labels() iter.Seq[environment.LabelWithOrigin] { return nil }

// FileListValue represents a processed file list value.
type FileListValue struct {
	origin *resolve.ListValue
	list   []fs.SourceFile
}

func (f FileListValue) value() resolve.Value {
	return f.origin
}
func (FileListValue) Labels() iter.Seq[environment.LabelWithOrigin] { return nil }

// LabelKeyedStringMapValue represents a value processed into a map,
// where the keys are labels and the values are strings.
//
// This can be thought of as similar to a label_keyed_string_dict in Bazel.
type LabelKeyedStringMapValue struct {
	origin *resolve.ScopeValue
	data   map[environment.Label]string
	// Required to implement the ProcessedValue interface, so that
	// the builder can later add deps on all labels referenced by
	// the target.
	labelWithOrigins []environment.LabelWithOrigin
}

func (f LabelKeyedStringMapValue) value() resolve.Value {
	return f.origin
}

func (f LabelKeyedStringMapValue) Labels() iter.Seq[environment.LabelWithOrigin] {
	return slices.Values(f.labelWithOrigins)
}

// ProcessedValueAs attempts to cast the value as the specified type, and returns an error if it fails.
func ProcessedValueAs[T ProcessedValue](v ProcessedValue) (T, error) {
	t, ok := v.(T)
	if ok {
		return t, nil
	}
	return t, environment.IllegalStateError{
		Reason: fmt.Sprintf(
			"Tried to use internally-processed value as %T even though it was saved as %T",
			new(T), v),
	}
}
