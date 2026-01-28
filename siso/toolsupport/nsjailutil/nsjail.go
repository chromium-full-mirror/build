// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package nsjailutil provides utilities for nsjail.
package nsjailutil

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/prototext"
	"google.golang.org/protobuf/proto"

	pb "go.chromium.org/build/siso/toolsupport/nsjailutil/proto"
)

// NSJail manages nsjail environment.
type NSJail struct {
	exePath string
	dir     string
	config  *pb.NsJailConfig
}

// Request is a request for nsjail.
type Request struct {
	// nsjail binary path.
	ExePath string `json:"nsjail_path"`

	// nsjail temporary scratch dir.
	JailRootDir string `json:"root_dir"`

	// absolute paths. e.g. /bin
	PublicDirs []string `json:"public_dirs,omitempty"`

	ExecRoot string `json:"exec_root"`     // absolute path.
	Dir      string `json:"dir,omitempty"` // working dir, relative to ExecRoot

	// relative to exec root.
	Inputs []string `json:"inputs"`

	// relative to exec root, or absolute path. read/write.
	OutDir string `json:"out_dir"`

	// relative to exec root, or absolute path
	Outputs []string `json:"outputs"`
}

// New creates nsjail environment with req from fsys at root dir.
func New(ctx context.Context, fsys fs.FS, req Request) (_ *NSJail, err error) {
	jail := &NSJail{
		config: &pb.NsJailConfig{},
	}
	defer func() {
		if err != nil && jail != nil && jail.dir != "" {
			os.RemoveAll(jail.dir)
		}
	}()
	jail.exePath, err = exec.LookPath(req.ExePath)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(req.JailRootDir) {
		return nil, fmt.Errorf("root_dir is not absolute path: %q", req.JailRootDir)
	}
	if !filepath.IsAbs(req.ExecRoot) {
		return nil, fmt.Errorf("exec_root is not absolute path: %q", req.ExecRoot)
	}
	if filepath.IsAbs(req.Dir) {
		return nil, fmt.Errorf("dir is absolute path: %q", req.Dir)
	}
	fsys, err = fs.Sub(fsys, strings.TrimPrefix(req.ExecRoot, "/"))
	if err != nil {
		return nil, err
	}
	jail.dir, err = os.MkdirTemp(req.JailRootDir, "nsjail.*")
	if err != nil {
		return nil, err
	}
	for _, output := range req.Outputs {
		outputPathInSandbox := output
		if !filepath.IsAbs(outputPathInSandbox) {
			outputPathInSandbox = filepath.Join(req.ExecRoot, output)
		}
		err := os.MkdirAll(filepath.Dir(filepath.Join(jail.dir, outputPathInSandbox)), 0755)
		if err != nil {
			return nil, err
		}
	}
	// equivalent to -q, for quiet execution
	jail.config.LogLevel = pb.LogLevel_WARNING.Enum()
	// disable all rlimits, default to limits set by parent.
	jail.config.DisableRl = proto.Bool(true)

	jail.config.Cwd = proto.String(filepath.Join(req.ExecRoot, req.Dir))
	// TODO: better environment variable sandboxing
	jail.config.KeepEnv = proto.Bool(true)

	if req.PublicDirs == nil {
		req.PublicDirs = []string{"/bin", "/lib", "/lib64", "/usr", "/dev"}
	}
	for _, dir := range req.PublicDirs {
		jail.config.Mount = append(jail.config.Mount, &pb.MountPt{
			Src:    proto.String(dir),
			Dst:    proto.String(dir),
			IsBind: proto.Bool(true),
			IsDir:  proto.Bool(true),
		})
	}
	jail.config.MountProc = proto.Bool(true)
	// Add a temp directory. Using a directory in the working directory
	// instead of a tmpfs mount so that we don't have to worry about how
	// big of a tmpfs to make.
	tmpDir := filepath.Join(jail.dir, "tmp")
	err = os.Mkdir(tmpDir, 0755)
	if err != nil {
		return nil, err
	}
	jail.config.Mount = append(jail.config.Mount, &pb.MountPt{
		Src:    proto.String(tmpDir),
		Dst:    proto.String("/tmp"),
		IsBind: proto.Bool(true),
		IsDir:  proto.Bool(true),
		Rw:     proto.Bool(true),
	})

	// Add the source directory. Normally this is not necessary,
	// because the -R flags for the input files would cause nsjail
	// to create it. But in cases where the action has no inputs
	// or all the inputs are from an absolute out/ directory,
	// it won't be created by nsjail automatically and the --cwd /src
	// flag will fail.
	srcDir := filepath.Join(jail.dir, req.ExecRoot)
	err = os.MkdirAll(srcDir, 0755)
	if err != nil {
		return nil, err
	}
	jail.config.Mount = append(jail.config.Mount, &pb.MountPt{
		Src:    proto.String(srcDir),
		Dst:    proto.String(req.ExecRoot),
		IsBind: proto.Bool(true),
		IsDir:  proto.Bool(true),
	})

	// Add the out directory. It needs to be in a location that still
	// matches all the output paths in the ninja file, so that we don't
	// need to rewrite those paths. So if the out directory is at an
	// absolute path, keep it in the same locaton. If it's at a
	// relative path, move it to the relative to /src/.
	outDirInSandbox := req.OutDir
	if !filepath.IsAbs(outDirInSandbox) {
		outDirInSandbox = filepath.Join(req.ExecRoot, outDirInSandbox)
	}
	absOutDir := filepath.Join(jail.dir, outDirInSandbox)
	err = os.MkdirAll(absOutDir, 0755)
	if err != nil {
		return nil, err
	}
	jail.config.Mount = append(jail.config.Mount, &pb.MountPt{
		Src:    proto.String(absOutDir),
		Dst:    proto.String(outDirInSandbox),
		IsBind: proto.Bool(true),
		IsDir:  proto.Bool(true),
		Rw:     proto.Bool(true),
	})

	for _, input := range req.Inputs {
		inputPath := filepath.Join(req.ExecRoot, input)
		fi, err := fs.Lstat(fsys, input)
		if err != nil {
			return nil, err
		}
		switch {
		case fi.Mode()&fs.ModeType == fs.ModeSymlink:
			// need to use symlink as symlink.
			// android uses dangling symlink, and
			// bind such dangling symlink will fail.
			target, err := fs.ReadLink(fsys, input)
			if err != nil {
				return nil, err
			}
			jail.config.Mount = append(jail.config.Mount, &pb.MountPt{
				Src:       proto.String(target),
				Dst:       proto.String(inputPath),
				IsSymlink: proto.Bool(true),
				IsDir:     proto.Bool(false),
			})
		case fi.Mode().IsRegular():
			jail.config.Mount = append(jail.config.Mount, &pb.MountPt{
				Src:    proto.String(inputPath),
				Dst:    proto.String(inputPath),
				IsBind: proto.Bool(true),
				IsDir:  proto.Bool(false),
			})
		default:
			return nil, fmt.Errorf("unsupported input file type %q: %v", input, fi.Mode())
		}
	}
	return jail, nil
}

// Dir returns jail dir.
func (j *NSJail) Dir() string {
	return j.dir
}

// Args creates nsjail config to run args and returns command line to run args under nsjail.
func (j *NSJail) Args(ctx context.Context, args ...string) ([]string, error) {
	config := proto.CloneOf(j.config)
	config.ExecBin = &pb.Exe{
		Path: proto.String(args[0]),
		Arg:  args[1:],
	}
	configData, err := prototext.Marshal(config)
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(j.dir, "nsjail.config")
	err = os.WriteFile(configPath, configData, 0755)
	if err != nil {
		return nil, err
	}
	return []string{j.exePath, "-C", configPath}, nil
}

// Close closes the jail and cleans up jail dir.
func (j *NSJail) Close() error {
	if j == nil || j.dir == "" {
		return nil
	}
	return os.RemoveAll(j.dir)
}
