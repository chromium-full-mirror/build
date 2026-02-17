// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

// VarType is the type of a target's variable, prior to processing.
// TODO: better to define type for varType and use method instead of switch by value?
type VarType uint8

const ( //                                 Internal [ProcessedValue] type
	// A StringType variable accepts single strings e.g.
	//	depfile = "$target_gen_dir/$target_name.d"
	StringType VarType = iota // ------------------- [StringValue]
	// A StringListType variable accepts lists of strings e.g.
	//	cflags = [ "-fvisibility=default" ]
	StringListType // ------------------------------ [StringListValue]
	// A TargetLabelListType variable accepts lists of targets e.g.
	//	deps = [ ":foo", "//bar:baz" ]
	TargetLabelListType // ------------------------- [LabelListValue]
	// A ConfigLabelListType variable accepts lists of configs e.g.
	//	configs = [ ":foo", "//bar:baz" ]
	ConfigLabelListType // ------------------------- [LabelListValue]
	// A FileType variable accepts single files e.g.
	//	script = "domything.py"
	FileType // ------------------------------------ [FileValue]
	// A FileListType variable accepts lists of files e.g.
	//	inputs = [ "helper_library.py" ]
	FileListType // -------------------------------- [FileListValue]
)

// A ProcessedValue represents a target's variable, after we've converted values such as labels
// into concrete underlying types.
type ProcessedValue interface {
	// value returns the origin AST value.
	value() resolve.Value
}

// StringValue represents a processed string value.
type StringValue struct {
	origin *resolve.StringValue
	str    string
}

func (f StringValue) value() resolve.Value {
	return f.origin
}

// StringListValue represents a processed string list value.
type StringListValue struct {
	origin *resolve.ListValue
	list   []string
}

func (f StringListValue) value() resolve.Value {
	return f.origin
}

// LabelListValue represents a processed label list value.
type LabelListValue struct {
	Origin *resolve.ListValue
	List   []environment.LabelWithOrigin
}

func (f LabelListValue) value() resolve.Value {
	return f.Origin
}

// FileValue represents a processed file value.
type FileValue struct {
	origin *resolve.StringValue
	file   fs.SourceFile
}

func (f FileValue) value() resolve.Value {
	return f.origin
}

// FileListValue represents a processed file list value.
type FileListValue struct {
	origin *resolve.ListValue
	list   []fs.SourceFile
}

func (f FileListValue) value() resolve.Value {
	return f.origin
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

// processValue takes a raw buildfile declaration and processes it into the internal representation.
func (t *Target) processValue(value resolve.Value, expectedType VarType) (ProcessedValue, error) {
	switch expectedType {
	case StringType:
		sv, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return nil, err
		}
		return StringValue{
			origin: sv,
			str:    sv.RawGNString(),
		}, nil
	case FileType:
		sv, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return nil, err
		}
		sourceFile, err := t.label.Dir.ResolveRelativeFile(sv.RawGNString())
		if err != nil {
			return nil, err
		}
		return FileValue{
			origin: sv,
			file:   sourceFile,
		}, nil
	case StringListType:
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
	case FileListType:
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
			sourceFile, err := t.label.Dir.ResolveRelativeFile(sv.RawGNString())
			if err != nil {
				return nil, err
			}
			list = append(list, sourceFile)
		}
		return FileListValue{
			origin: lv,
			list:   list,
		}, nil
	case TargetLabelListType,
		ConfigLabelListType:
		// Both label list types collapse into [LabelListValue], since [Builder] will check deps for validity.
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
			resolvedLabel, err := environment.ResolveLabel(t.label.Dir, t.label.ToolchainLabel(), sv)
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
	return nil, environment.IllegalStateError{
		Reason: fmt.Sprintf("non-exhaustive switch over target variable types. expectedType: %d", expectedType),
	}
}
