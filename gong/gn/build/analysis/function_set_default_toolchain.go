// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"fmt"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

type setDefaultToolchainFunction struct {
}

func (setDefaultToolchainFunction) IsTarget() bool { return false }
func (setDefaultToolchainFunction) HelpShort() string {
	return "set_default_toolchain: Sets the default toolchain name."
}
func (setDefaultToolchainFunction) Help() string {
	return `set_default_toolchain: Sets the default toolchain name.

  set_default_toolchain(toolchain_label)

  The given label should identify a toolchain definition (see "gn help
  toolchain"). This toolchain will be used for all targets unless otherwise
  specified.

  This function is only valid to call during the processing of the build
  configuration file. Since the build configuration file is processed
  separately for each toolchain, this function will be a no-op when called
  under any non-default toolchains.

  For example, the default toolchain should be appropriate for the current
  environment. If the current environment is 32-bit and somebody references a
  target with a 64-bit toolchain, we wouldn't want processing of the build
  config file for the 64-bit toolchain to reset the default toolchain to
  64-bit, we want to keep it 32-bits.

Argument

  toolchain_label
      Toolchain name.

Example

  # Set default toolchain only has an effect when run in the context of the
  # default toolchain. Pick the right one according to the current CPU
  # architecture.
  if (target_cpu == "x64") {
    set_default_toolchain("//toolchains:64")
  } else if (target_cpu == "x86") {
    set_default_toolchain("//toolchains:32")
  }
`
}

func (setDefaultToolchainFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	ctx, ok := scope.ExecContext().(*scopeContext)
	if !ok {
		return nil, fmt.Errorf("internal error: received a scope without a scopeContext")
	}

	if !ctx.isProcessingBuildConfig() {
		return nil, call.Function.MakeErrorWithHelp(
			syntax.ErrInvalidOperation,
			"Must be called from build config.",
			"set_default_toolchain can only be called from the build configuration file.",
		)
	}

	// TODO: we should be a noop if the loader isn't expecting a default toolchain to be set (because it's already been set)
	// https://source.chromium.org/gn/gn/+/main:src/gn/function_set_default_toolchain.cc;l=66-71;drc=a899709c3b024eddade4cf7eab167b5962164fb0

	input, err := resolve.EnsureSingleStringArg(call, args)
	if err != nil {
		return nil, err
	}

	// TODO: Scope doesn't support current directory yet so pretend we're in the root.
	fakeWd, err := fs.MakeSourceDir("//")
	if err != nil {
		return nil, err
	}

	// TODO: C++ GN retrieves the "default toolchain" from the Scope (this isn't implemented yet)
	// and passes it to ResolveLabel. But we call set_default_toolchain when we don't know the default toolchain yet,
	// so is that actually necessary? For now just pass an empty label as the "current toolchain".
	// https://source.chromium.org/gn/gn/+/main:src/gn/function_set_default_toolchain.cc;l=80;drc=a899709c3b024eddade4cf7eab167b5962164fb0
	toolchainLabel, err := environment.ResolveLabel(fakeWd, environment.Label{}, input)
	if err != nil {
		return nil, err
	}

	// If the default toolchain receiver is set, call it.
	// If it isn't set, it means we're not processing the default toolchain, therefore the label is already known.
	// (Hence just no-op and continue processing the rest of the buildconfig file.)
	if ctx.defaultToolchainReceiver != nil {
		ctx.defaultToolchainReceiver(toolchainLabel)
	}
	return nil, nil
}
