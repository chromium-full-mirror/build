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

// varType is the type of a target's variable, prior to processing.
type varType uint8

const ( //                           Internal [processedValue] type
	stringType          varType = iota // [stringValue]
	stringListType                     // [stringListValue]
	targetLabelListType                // [labelListValue]
	configLabelListType                // [labelListValue]
	fileType                           // [fileValue]
	fileListType                       // [fileListValue]
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
func (t *Target) processValue(value resolve.Value, expectedType varType) (processedValue, error) {
	switch expectedType {
	case stringType:
		sv, err := resolve.AsValue[*resolve.StringValue](value)
		if err != nil {
			return nil, err
		}
		return stringValue{
			origin: sv,
			str:    sv.RawGNString(),
		}, nil
	case fileType:
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
	case stringListType:
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
	case fileListType:
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
	case targetLabelListType,
		configLabelListType:
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
			resolvedLabel, err := environment.ResolveLabel(t.label.Dir, environment.Label{}, sv)
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
