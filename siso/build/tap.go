// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	log "github.com/golang/glog"

	"go.chromium.org/build/siso/execute"
	"go.chromium.org/build/siso/o11y/clog"
)

type tapFactory interface {
	New(context.Context, *Builder, execute.Executor) (execute.Executor, error)
}

// TODO: integrate cartfs

type externalTapFactory struct {
	tapCommand string
}

func newExternalTapFactory() (externalTapFactory, error) {
	s := os.Getenv("SISO_TAP_COMMAND")
	if s == "" {
		return externalTapFactory{}, errors.New("no SISO_TAP_COMMAND")
	}
	return externalTapFactory{
		tapCommand: s,
	}, nil
}

func (f externalTapFactory) New(ctx context.Context, b *Builder, executor execute.Executor) (execute.Executor, error) {
	if f.tapCommand == "" {
		return nil, fmt.Errorf("no external tap command. need SISO_TAP_COMMAND")
	}
	return &externalTapExecutor{
		b:          b,
		tapCommand: f.tapCommand,
		executor:   executor,
	}, nil
}

type externalTapExecutor struct {
	b           *Builder
	tapCommand  string
	executor    execute.Executor
	origInputs  []string
	origOutputs []string
	inputs      []string
	outputs     []string
}

func (t *externalTapExecutor) Run(ctx context.Context, cmd *execute.Cmd) error {
	cmd.StdoutWriter()
	cmd.StderrWriter()
	tapLogFile, err := os.CreateTemp("", fmt.Sprintf("tap-%s-*.json", cmd.ID))
	if err != nil {
		return err
	}
	defer func() {
		os.Remove(tapLogFile.Name())
	}()
	newCmd := &execute.Cmd{}
	*newCmd = *cmd
	newCmd.Args = append([]string{
		t.tapCommand,
		"--tap_output", tapLogFile.Name(),
		"--",
	}, cmd.Args...)
	err = t.executor.Run(ctx, newCmd)
	if err != nil {
		return err
	}
	cmd.SetActionResult(newCmd.ActionResult())
	t.origInputs = cmd.Inputs
	t.origOutputs = cmd.Outputs
	t.inputs, t.outputs, err = t.postProcess(ctx, tapLogFile.Name(), cmd)
	if err == nil {
		cmd.Pure = true
	}
	if log.V(2) {
		clog.Infof(ctx, "inputs %q", cmd.Inputs)
		clog.Infof(ctx, "outputs %q", cmd.Outputs)
	}
	return err
}

func (t *externalTapExecutor) postProcess(ctx context.Context, tapLogFileName string, cmd *execute.Cmd) (inputs, outputs []string, retErr error) {
	buf, err := os.ReadFile(tapLogFileName)
	if err != nil {
		return nil, nil, err
	}
	type tapOutput struct {
		Reads   []string `json:"reads,omitempty"`
		Writes  []string `json:"writes,omitempty"`
		Deletes []string `json:"deletes,omitempty"`
	}
	var tapData tapOutput
	err = json.Unmarshal(buf, &tapData)
	if err != nil {
		return nil, nil, err
	}
	// ignore out of workspace root
	// TODO: use with input root absolute path?
	// TODO: just use detected inputs?
	seen := make(map[string]bool)
	for _, input := range cmd.AllInputs() {
		seen[input] = true
	}
	for _, input := range tapData.Reads {
		rel, err := filepath.Rel(t.b.path.WorkspaceRoot, input)
		if err != nil {
			clog.Warningf(ctx, "reads relpath %q: %v", input, err)
			continue
		}
		if !filepath.IsLocal(rel) {
			continue
		}
		_, err = t.b.hashFS.Stat(ctx, t.b.path.WorkspaceRoot, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		inputs = append(inputs, rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		cmd.Inputs = append(cmd.Inputs, rel)
	}
	clear(seen)
	// need to use both original outputs and detected outputs.
	// it might not detect output for restat action.
	// it might detect unspecified outputs.
	for _, output := range cmd.AllOutputs() {
		seen[output] = true
	}
	for _, output := range tapData.Writes {
		rel, err := filepath.Rel(t.b.path.WorkspaceRoot, output)
		if err != nil {
			clog.Warningf(ctx, "writes relpath %q: %v", output, err)
			continue
		}
		if !filepath.IsLocal(rel) {
			continue
		}
		fi, err := t.b.hashFS.Stat(ctx, t.b.path.WorkspaceRoot, rel)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			// don't include output directories
			// as it would forget all entries in the directory
			// by hashfs Update.
			continue
		}
		outputs = append(outputs, rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		cmd.Outputs = append(cmd.Outputs, rel)
	}
	// TODO: handle deletes
	for _, del := range tapData.Deletes {
		rel, err := filepath.Rel(t.b.path.WorkspaceRoot, del)
		if err != nil {
			clog.Warningf(ctx, "deletes relpath %q: %v", del, err)
			continue
		}
		if !filepath.IsLocal(rel) {
			continue
		}
		_, err = t.b.hashFS.Stat(ctx, t.b.path.WorkspaceRoot, rel)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		clog.Infof(ctx, "delete %q", rel)
	}
	return inputs, outputs, nil
}

// TODO: implement?
// func (t *externalTapExecutor) logLocalExec(ctx context.Context, step *Step, dur time.Duration) error {
// }
