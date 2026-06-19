// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"os"
	"strings"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

type getenvFunction struct{}

func (getenvFunction) HelpShort() string { return "getenv: Get an environment variable." }

func (getenvFunction) Help() string {
	return `getenv: Get an environment variable.

  value = getenv(env_var_name)

  Returns the value of the given environment variable. If the value is not
  found, it will try to look up the variable with the "opposite" case (based on
  the case of the first letter of the variable), but is otherwise
  case-sensitive.

  If the environment variable is not found, the empty string will be returned.
  Note: it might be nice to extend this if we had the concept of "none" in the
  language to indicate lookup failure.

Example

  home_dir = getenv("HOME")`
}

func (getenvFunction) IsTarget() bool { return false }

func (getenvFunction) Run(scope *resolve.Scope, call *parse.FunctionCallNode, args []resolve.Value) (resolve.Value, error) {
	strVal, err := resolve.EnsureSingleStringArg(call, args)
	if err != nil {
		return nil, err
	}

	envVar := strVal.RawGNString()
	if val, found := os.LookupEnv(envVar); found {
		return resolve.NewOriginlessStringValue(val), nil
	}

	// Fallback to opposite case.
	var alt string
	if len(envVar) > 0 {
		switch c := envVar[0]; {
		case c >= 'a' && c <= 'z':
			alt = strings.ToUpper(envVar)
		case c >= 'A' && c <= 'Z':
			alt = strings.ToLower(envVar)
		}
	}
	if alt != "" {
		if val, found := os.LookupEnv(alt); found {
			return resolve.NewOriginlessStringValue(val), nil
		}
	}

	// Still couldn't find; return empty string.
	return resolve.NewOriginlessStringValue(""), nil
}
