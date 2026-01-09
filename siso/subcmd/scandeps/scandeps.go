// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package scandeps is scandeps subcommand for debugging scandeps.
package scandeps

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	"github.com/google/subcommands"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/buildconfig"
	"go.chromium.org/build/siso/build/ninjabuild"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/scandeps"
	"go.chromium.org/build/siso/toolsupport/gccutil"
	"go.chromium.org/build/siso/toolsupport/msvcutil"
	"go.chromium.org/build/siso/toolsupport/shutil"
)

const usage = `run scandeps

 $ siso scandeps -C <dir> -req '<json scandeps request>'
 $ siso scandeps -C <dir> -target <build target name>

<json scandeps request> can be found in siso.INFO log
for "scandeps failed Request". you can copy-and-paste
the json string from the log.
Or you can manually construct json string of
infra/build/siso/scandeps.Request.
`

// Cmd returns the Command for the `scandeps` subcommand provided by this package.
func Cmd() *Command {
	return &Command{}
}

func (*Command) Name() string {
	return "scandeps"
}

func (*Command) Synopsis() string {
	return "run scandeps"
}

func (*Command) Usage() string {
	return usage
}

// Command implements scandeps subcommand.
type Command struct {
	execRoot      string
	buildDir      string
	stateDir      string
	reqJSONString string
	targetName    string
}

func (c *Command) SetFlags(flagSet *flag.FlagSet) {
	flagSet.StringVar(&c.buildDir, "C", ".", "ninja running directory to find .siso_config and .siso_filegroup for input_deps in state dir")
	flagSet.StringVar(&c.stateDir, "state_dir", ".", "state directory (relative to -C)")
	flagSet.StringVar(&c.reqJSONString, "req", "", "json format of scandeps request")
	flagSet.StringVar(&c.targetName, "target", "", "build target name")
}

func (c *Command) absBuildDir() string {
	if filepath.IsAbs(c.buildDir) {
		return c.buildDir
	}
	return filepath.Join(c.execRoot, c.buildDir)
}

func (c *Command) absStateDir() string {
	if filepath.IsAbs(c.stateDir) {
		return c.stateDir
	}
	return filepath.Join(c.absBuildDir(), c.stateDir)
}

func (c *Command) Execute(ctx context.Context, flagSet *flag.FlagSet, _ ...any) subcommands.ExitStatus {
	var err error
	c.execRoot, err = os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		return subcommands.ExitFailure
	}

	err = c.run(ctx)
	if err != nil {
		switch {
		case errors.Is(err, flag.ErrHelp):
			fmt.Fprintf(os.Stderr, "%v\n%s", err, usage)
			return subcommands.ExitUsageError
		default:
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			return subcommands.ExitFailure
		}
	}
	return subcommands.ExitSuccess
}

func (c *Command) run(ctx context.Context) error {
	req, err := c.createRequest(ctx)
	if err != nil {
		return err
	}
	return c.scanWithRequest(ctx, req)
}

func (c *Command) createRequest(ctx context.Context) (scandeps.Request, error) {
	if c.reqJSONString != "" {
		var req scandeps.Request
		err := json.Unmarshal([]byte(c.reqJSONString), &req)
		return req, err
	}
	if c.targetName != "" {
		return c.createRequestFromTarget(ctx)
	}
	return scandeps.Request{}, fmt.Errorf("missing req or target: %w", flag.ErrHelp)
}

func (c *Command) createRequestFromTarget(ctx context.Context) (scandeps.Request, error) {
	buildNinjaPath := filepath.Join(c.absBuildDir(), "build.ninja")
	relBuildDir, err := filepath.Rel(c.execRoot, c.absBuildDir())
	if err != nil {
		return scandeps.Request{}, fmt.Errorf("failed to get relative build dir: %w", err)
	}
	pathCtx := build.NewPath(c.execRoot, relBuildDir)

	nstate, err := ninjabuild.Load(ctx, buildNinjaPath, pathCtx)
	if err != nil {
		return scandeps.Request{}, fmt.Errorf("failed to load %s: %w", buildNinjaPath, err)
	}

	node, ok := nstate.LookupNodeByPath(c.targetName)
	if !ok {
		return scandeps.Request{}, fmt.Errorf("target %q not found in %s", c.targetName, buildNinjaPath)
	}
	edge, ok := node.InEdge()
	if !ok {
		return scandeps.Request{}, fmt.Errorf("target %q is a source file or has no build edge", c.targetName)
	}

	cmdLineStr := edge.Binding("command")
	if cmdLineStr == "" {
		return scandeps.Request{}, fmt.Errorf("no command found for target %q", c.targetName)
	}

	cmdLine, err := shutil.Split(cmdLineStr)
	if err != nil {
		return scandeps.Request{}, fmt.Errorf("failed to split command line: %q: %w", cmdLineStr, err)
	}

	if len(cmdLine) == 0 {
		return scandeps.Request{}, fmt.Errorf("empty command line for target %q", c.targetName)
	}

	fsys := os.DirFS(c.absBuildDir())

	var rule ninjabuild.StepRule
	timeout := 2 * time.Minute
	stepConfig, err := c.loadStepConfig()
	if err != nil {
		clog.Warningf(ctx, "failed to load step config: %v", err)
	} else {
		r, ok := stepConfig.Lookup(ctx, pathCtx, edge)
		if ok {
			rule = r
			if rule.Timeout != "" {
				d, err := time.ParseDuration(rule.Timeout)
				if err == nil {
					timeout = d
				}
			}
		}
	}

	switch edge.Binding("deps") {
	case "msvc":
		params, err := msvcutil.ExtractScanDepsParams(ctx, cmdLine, nil, fsys)
		if err != nil {
			return scandeps.Request{}, fmt.Errorf("failed to extract msvc scandeps params for target %q: %w", c.targetName, err)
		}
		req, err := build.CreateScanDepsRequestMSVC(ctx, pathCtx, params, rule.Platform, rule.UseSystemInput, timeout)
		return req, err
	case "gcc":
		params, err := gccutil.ExtractScanDepsParams(ctx, cmdLine, nil, fsys)
		if err != nil {
			return scandeps.Request{}, fmt.Errorf("failed to extract gcc scandeps params for target %q: %w", c.targetName, err)
		}
		req, _, err := build.CreateScanDepsRequestGCC(ctx, pathCtx, params, rule.Platform, rule.UseSystemInput, timeout)
		return req, err
	default:
		return scandeps.Request{}, fmt.Errorf("unsupported deps %q for target %q", edge.Binding("deps"), c.targetName)
	}
}

func (c *Command) scanWithRequest(ctx context.Context, req scandeps.Request) error {
	buf, err := json.MarshalIndent(req, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("request=%s\n", buf)
	inputDeps, err := c.loadInputDeps()
	if err != nil {
		return err
	}
	clog.Infof(ctx, "input_deps=%q\n", inputDeps)

	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		return err
	}

	s := scandeps.New(hashFS, inputDeps, nil)

	result, err := s.Scan(ctx, c.execRoot, req)
	if err != nil {
		return err
	}
	for _, r := range result {
		fmt.Println(r)
	}
	return nil
}

func (c *Command) loadInputDeps() (map[string][]string, error) {
	sc, err := c.loadStepConfig()
	if err != nil {
		return nil, err
	}
	return sc.InputDeps, nil
}

func (c *Command) loadStepConfig() (*ninjabuild.StepConfig, error) {
	stateDir := c.absStateDir()
	buf, err := os.ReadFile(filepath.Join(stateDir, ".siso_config"))
	if err != nil {
		return nil, err
	}
	var stepConfig ninjabuild.StepConfig
	err = json.Unmarshal(buf, &stepConfig)
	if err != nil {
		return nil, fmt.Errorf("load %s/.siso_config: %w", stateDir, err)
	}
	if stepConfig.InputDeps == nil {
		stepConfig.InputDeps = make(map[string][]string)
	}

	buf, err = os.ReadFile(filepath.Join(stateDir, ".siso_filegroups"))
	if err != nil {
		return nil, fmt.Errorf("load %s/.filegroups: %w", stateDir, err)
	}
	var filegroups buildconfig.Filegroups
	err = json.Unmarshal(buf, &filegroups)
	if err != nil {
		return nil, fmt.Errorf("load %s/.filegroups: %w", stateDir, err)
	}
	maps.Copy(stepConfig.InputDeps, filegroups.Filegroups)
	return &stepConfig, nil
}
