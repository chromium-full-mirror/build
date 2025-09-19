// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package model contains our data model for actions and functions to convert from/to REAPI types.
package model

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
)

type EnvVar struct {
	Name  string
	Value string
}

// Action contains all the information needed to execute an action.
type Action struct {
	// Digests for the action, command, and input root.
	ActionDigest    digest.Digest
	CommandDigest   digest.Digest
	InputRootDigest digest.Digest

	// Parameters influencing the use of caching.
	DoNotCache bool

	// Parameters influencing the execution.
	Args           []string
	EnvVars        []EnvVar
	WorkingDir     string
	OutputPaths    []string
	Timeout        time.Duration
	Platform       map[string][]string
	ContainerImage string
}

// LoadAction loads an Action from the CAS given its digest.
func LoadAction(d *repb.Digest, cas *blobstore.ContentAddressableStorage) (ka *Action, err error) {
	// Fetch the Action from the CAS.
	actionDigest, err := digest.NewFromProto(d)
	if err != nil {
		return nil, err
	}
	action, err := cas.Action(actionDigest)
	if err != nil {
		return nil, err
	}

	ka = &Action{
		ActionDigest: actionDigest,
		DoNotCache:   action.DoNotCache,
	}

	// Verify that the timeout is well-formed.
	if action.Timeout != nil {
		err := action.Timeout.CheckValid()
		if err != nil {
			return nil, fmt.Errorf("timeout is not a valid duration: %w", err)
		}
		ka.Timeout = action.Timeout.AsDuration()
	}

	// Fetch the Command from the CAS.
	ka.CommandDigest, err = digest.NewFromProto(action.CommandDigest)
	if err != nil {
		return nil, err
	}
	cmd, err := cas.Command(ka.CommandDigest)
	if err != nil {
		return nil, err
	}
	ka.Args = cmd.Arguments

	// Build a map of environment variables.
	prevName := ""
	for _, v := range cmd.EnvironmentVariables {
		if v.Name <= prevName {
			return nil, fmt.Errorf("environment variable names must be sorted and unique, but %q <= %q", v.Name, prevName)
		}
		prevName = v.Name

		ka.EnvVars = append(ka.EnvVars, EnvVar{
			Name:  v.Name,
			Value: v.Value,
		})
	}

	// Build a map of platform properties. REAPI v2.2+ says that the action's platform
	// properties take precedence over the command's, if both are present.
	p := action.Platform
	if p == nil {
		p = cmd.GetPlatform() //nolint:staticcheck
	}
	ka.Platform = make(map[string][]string)
	if p != nil {
		prevName = ""
		for _, prop := range p.Properties {
			// Note: Duplicate property names are explicitly allowed in the spec.
			if prop.Name < prevName {
				return nil, fmt.Errorf("platform properties must be sorted by name, but %q < %q", prop.Name, prevName)
			}
			prevName = prop.Name

			ka.Platform[prop.Name] = append(ka.Platform[prop.Name], prop.Value)
		}
	}

	if containerImages, ok := ka.Platform["container-image"]; ok {
		if len(containerImages) != 1 {
			return nil, fmt.Errorf("platform property container-image must have exactly one value, but got %d", len(containerImages))
		}
		ka.ContainerImage = containerImages[0]
	}

	// Validate the working directory.
	ka.WorkingDir = cmd.WorkingDirectory
	if ka.WorkingDir == "" {
		ka.WorkingDir = "."
	}
	if ka.WorkingDir != filepath.Clean(ka.WorkingDir) {
		return nil, fmt.Errorf("working directory is not a clean path, wanted %q, got %q", ka.WorkingDir, cmd.WorkingDirectory)
	}

	ka.InputRootDigest, err = digest.NewFromProto(action.InputRootDigest)
	if err != nil {
		return nil, err
	}

	// REAPI v2.0 clients specify output files and directories in two separate fields.
	ka.OutputPaths = cmd.OutputPaths
	if ka.OutputPaths == nil {
		ka.OutputPaths = make([]string, 0, len(cmd.OutputFiles)+len(cmd.OutputDirectories)) //nolint:staticcheck
		ka.OutputPaths = append(ka.OutputPaths, cmd.OutputFiles...)                         //nolint:staticcheck
		ka.OutputPaths = append(ka.OutputPaths, cmd.OutputDirectories...)                   //nolint:staticcheck
	}

	return ka, nil
}
