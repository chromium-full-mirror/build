// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

// VarType is the type of a target's variable, prior to processing.
// TODO: better to define type for varType and use method instead of switch by value?
type VarType uint8

const ( //                                 Internal [processedValue] type
	// A StringType variable accepts single strings e.g.
	//	depfile = "$target_gen_dir/$target_name.d"
	StringType VarType = iota // ------------------- [stringValue]
	// A StringListType variable accepts lists of strings e.g.
	//	cflags = [ "-fvisibility=default" ]
	StringListType // ------------------------------ [stringListValue]
	// A TargetLabelListType variable accepts lists of targets e.g.
	//	deps = [ ":foo", "//bar:baz" ]
	TargetLabelListType // ------------------------- [labelListValue]
	// A ConfigLabelListType variable accepts lists of configs e.g.
	//	configs = [ ":foo", "//bar:baz" ]
	ConfigLabelListType // ------------------------- [labelListValue]
	// A FileType variable accepts single files e.g.
	//	script = "domything.py"
	FileType // ------------------------------------ [fileValue]
	// A FileListType variable accepts lists of files e.g.
	//	inputs = [ "helper_library.py" ]
	FileListType // -------------------------------- [fileListValue]
)

// A processedValue represents a target's variable, after we've converted values such as labels
// into concrete underlying types.
type processedValue interface {
	// value returns the origin AST value.
	value() resolve.Value
}

type stringValue struct {
	origin *resolve.StringValue
	str    string
}

func (f stringValue) value() resolve.Value {
	return f.origin
}

type stringListValue struct {
	origin *resolve.ListValue
	list   []string
}

func (f stringListValue) value() resolve.Value {
	return f.origin
}

type labelListValue struct {
	origin *resolve.ListValue
	list   []environment.LabelWithOrigin
}

func (f labelListValue) value() resolve.Value {
	return f.origin
}

type fileValue struct {
	origin *resolve.StringValue
	file   fs.SourceFile
}

func (f fileValue) value() resolve.Value {
	return f.origin
}

type fileListValue struct {
	origin *resolve.ListValue
	list   []fs.SourceFile
}

func (f fileListValue) value() resolve.Value {
	return f.origin
}

func processedValueAs[T processedValue](v processedValue) (T, error) {
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
func (t *Target) processValue(value resolve.Value, expectedType VarType) (processedValue, error) {
	switch expectedType {
	case StringType:
		sv, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return nil, err
		}
		return stringValue{
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
		return fileValue{
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
		return stringListValue{
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
		return fileListValue{
			origin: lv,
			list:   list,
		}, nil
	case TargetLabelListType,
		ConfigLabelListType:
		// Both label list types collapse into [labelListValue], since [Builder] will check deps for validity.
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
		return labelListValue{
			origin: lv,
			list:   list,
		}, nil
	}
	return nil, environment.IllegalStateError{
		Reason: fmt.Sprintf("non-exhaustive switch over target variable types. expectedType: %d", expectedType),
	}
}
