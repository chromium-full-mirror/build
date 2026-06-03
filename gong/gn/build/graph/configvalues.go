// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"strings"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
)

// ConfigValues holds GN config() values.
type ConfigValues struct {
	Arflags        []string
	Asmflags       []string
	Cflags         []string
	CflagsC        []string
	CflagsCC       []string
	CflagsObjC     []string
	CflagsObjCC    []string
	Defines        []string
	Frameworks     []string
	WeakFrameworks []string
	Ldflags        []string
	Libs           []string
	Rustflags      []string
	Rustenv        []string
	Swiftflags     []string
	Inputs         []fs.SourceFile
}

// MakeConfigValues creates a new ConfigValues from a map of ProcessedValues.
func MakeConfigValues(values map[string]ProcessedValue) (ConfigValues, error) {
	var err error
	cv := ConfigValues{}
	if cv.Arflags, err = extractStringList(values, "arflags"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Asmflags, err = extractStringList(values, "asmflags"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Cflags, err = extractStringList(values, "cflags"); err != nil {
		return ConfigValues{}, err
	}
	if cv.CflagsC, err = extractStringList(values, "cflags_c"); err != nil {
		return ConfigValues{}, err
	}
	if cv.CflagsCC, err = extractStringList(values, "cflags_cc"); err != nil {
		return ConfigValues{}, err
	}
	if cv.CflagsObjC, err = extractStringList(values, "cflags_objc"); err != nil {
		return ConfigValues{}, err
	}
	if cv.CflagsObjCC, err = extractStringList(values, "cflags_objcc"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Defines, err = extractStringList(values, "defines"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Frameworks, err = extractFrameworkList(values, "frameworks"); err != nil {
		return ConfigValues{}, err
	}
	if cv.WeakFrameworks, err = extractFrameworkList(values, "weak_frameworks"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Ldflags, err = extractStringList(values, "ldflags"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Libs, err = extractStringList(values, "libs"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Rustflags, err = extractStringList(values, "rustflags"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Rustenv, err = extractStringList(values, "rustenv"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Swiftflags, err = extractStringList(values, "swiftflags"); err != nil {
		return ConfigValues{}, err
	}
	if cv.Inputs, err = extractFileList(values, "inputs"); err != nil {
		return ConfigValues{}, err
	}
	return cv, nil
}

func extractStringList(values map[string]ProcessedValue, key string) ([]string, error) {
	if v, ok := values[key]; ok {
		slv, err := ProcessedValueAs[StringListValue](v)
		if err != nil {
			return nil, err
		}
		return slv.list, nil
	}
	return nil, nil
}

// extractFrameworkList is the same as extractStringList, but also validates framework values.
func extractFrameworkList(values map[string]ProcessedValue, key string) ([]string, error) {
	if v, ok := values[key]; ok {
		slv, err := ProcessedValueAs[StringListValue](v)
		if err != nil {
			return nil, err
		}
		// All strings must end with ".framework".
		for _, str := range slv.list {
			if !strings.HasSuffix(str, ".framework") {
				return nil, &FrameworkMissingExtension{
					OriginValue: resolve.OriginValue{Value: v.value()},
					framework:   str,
				}
			}
		}
		return slv.list, nil
	}
	return nil, nil
}

func extractFileList(values map[string]ProcessedValue, key string) ([]fs.SourceFile, error) {
	if v, ok := values[key]; ok {
		flv, err := ProcessedValueAs[FileListValue](v)
		if err != nil {
			return nil, err
		}
		return flv.list, nil
	}
	return nil, nil
}

// Append appends the values from the other config objects to this one.
func (c *ConfigValues) Append(configDeps ...*Config) error {
	for _, subConfig := range configDeps {
		c.appendValues(subConfig.resolvedValues)
	}
	return nil
}

func (c *ConfigValues) appendValues(appendVals *ConfigValues) {
	if appendVals == nil {
		return
	}
	c.Arflags = append(c.Arflags, appendVals.Arflags...)
	c.Asmflags = append(c.Asmflags, appendVals.Asmflags...)
	c.Cflags = append(c.Cflags, appendVals.Cflags...)
	c.CflagsC = append(c.CflagsC, appendVals.CflagsC...)
	c.CflagsCC = append(c.CflagsCC, appendVals.CflagsCC...)
	c.CflagsObjC = append(c.CflagsObjC, appendVals.CflagsObjC...)
	c.CflagsObjCC = append(c.CflagsObjCC, appendVals.CflagsObjCC...)
	c.Defines = append(c.Defines, appendVals.Defines...)
	c.Frameworks = append(c.Frameworks, appendVals.Frameworks...)
	c.WeakFrameworks = append(c.WeakFrameworks, appendVals.WeakFrameworks...)
	c.Ldflags = append(c.Ldflags, appendVals.Ldflags...)
	c.Libs = append(c.Libs, appendVals.Libs...)
	c.Rustflags = append(c.Rustflags, appendVals.Rustflags...)
	c.Rustenv = append(c.Rustenv, appendVals.Rustenv...)
	c.Swiftflags = append(c.Swiftflags, appendVals.Swiftflags...)
	c.Inputs = append(c.Inputs, appendVals.Inputs...)
}
