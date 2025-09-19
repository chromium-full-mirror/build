// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/bazelbuild/remote-apis-sdks/go/pkg/digest"
	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/kajiya/blobstore"
)

// Executor is a local executor that executes actions on the local machine.
// It uses a ContentAddressableStorage to fetch all required inputs for the action
// into a sandbox directory, executes the action in that sandbox directory, and
// then uploads the output files and directories to the CAS after the action has finished.
type Executor struct {
	cas         *blobstore.ContentAddressableStorage
	images      *ImageRepository
	nsjailPath  string
	sandboxBase string
	trees       *TreeRepository
}

// New creates a new Executor.
// baseDir is a directory used to store temporary files required during execution, such as
// sandbox directories.
// cas is the ContentAddressableStorage to use for fetching and uploading blobs.
func New(baseDir string, cas *blobstore.ContentAddressableStorage) (*Executor, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir must be set")
	}

	if cas == nil {
		return nil, fmt.Errorf("cas must be set")
	}

	// Create the data directory if it doesn't exist.
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %q: %w", baseDir, err)
	}

	images, err := NewImageRepository(filepath.Join(baseDir, "images"))
	if err != nil {
		return nil, fmt.Errorf("failed to create image repository: %w", err)
	}

	sandboxBase := filepath.Join(baseDir, "tmp")
	if err := os.MkdirAll(sandboxBase, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %q: %w", sandboxBase, err)
	}

	// Try to find nsjail in the PATH.
	nsjailPath := ""
	if runtime.GOOS == "linux" {
		nsjailPath, err = exec.LookPath("nsjail")
		if err != nil {
			return nil, fmt.Errorf("🚨 required tool 'nsjail' not found in PATH")
		}
	}

	trees, err := newTreeRepository(filepath.Join(baseDir, "trees"), cas)
	if err != nil {
		return nil, fmt.Errorf("failed to create tree repository: %w", err)
	}

	return &Executor{
		cas:         cas,
		images:      images,
		nsjailPath:  nsjailPath,
		sandboxBase: sandboxBase,
		trees:       trees,
	}, nil
}

// Execute executes the given action and returns the result.
func (e *Executor) Execute(action *repb.Action) (*repb.ActionResult, error) {
	// Get the command from the CAS.
	cmdDigest, err := digest.NewFromProto(action.CommandDigest)
	if err != nil {
		return nil, fmt.Errorf("failed to parse command digest: %w", err)
	}
	cmd, err := e.cas.Command(cmdDigest)
	if err != nil {
		return nil, err
	}
	if cmd.WorkingDirectory != "" {
		if !filepath.IsLocal(cmd.WorkingDirectory) {
			return nil, fmt.Errorf("working directory %q points outside of input root", cmd.WorkingDirectory)
		}
	}
	// REAPI v2.0 clients specify output files and directories in two separate fields. In order
	// to simplify our code, we just merge them into REAPI v2.1+'s OutputPaths field here.
	if cmd.OutputPaths == nil {
		cmd.OutputPaths = make([]string, 0, len(cmd.OutputFiles)+len(cmd.OutputDirectories)) //nolint:staticcheck
		cmd.OutputPaths = append(cmd.OutputPaths, cmd.OutputFiles...)                        //nolint:staticcheck
		cmd.OutputPaths = append(cmd.OutputPaths, cmd.OutputDirectories...)                  //nolint:staticcheck
	}

	// Build a sandbox directory for the action.
	sandboxDir, err := os.MkdirTemp(e.sandboxBase, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox directory: %w", err)
	}
	defer e.deleteSandbox(sandboxDir)

	sb := &Sandbox{
		cas:        e.cas,
		trees:      e.trees,
		sandboxDir: sandboxDir,
	}

	// Stage the input files and directories into the sandbox.
	if err := sb.Prepare(action, cmd); err != nil {
		return nil, fmt.Errorf("failed to prepare input root: %w", err)
	}

	// Execute the command.
	actionResult, err := e.executeCommand(sb, action, cmd)
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	// Save stdout and stderr to the CAS and update their digests in the action result.
	if err := e.saveStdOutErr(actionResult); err != nil {
		return nil, err
	}

	// Go through all output files and directories and upload them to the CAS.
	if err := sb.UploadOutputs(cmd, actionResult); err != nil {
		return nil, fmt.Errorf("failed to upload outputs: %w", err)
	}

	return actionResult, nil
}

// saveStdOutErr saves stdout and stderr to the CAS and returns the updated action result.
func (e *Executor) saveStdOutErr(actionResult *repb.ActionResult) error {
	d, err := e.cas.Put(actionResult.StdoutRaw)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to put stdout into CAS: %v", err)
	}
	actionResult.StdoutDigest = d.ToProto()

	d, err = e.cas.Put(actionResult.StderrRaw)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to put stderr into CAS: %v", err)
	}
	actionResult.StderrDigest = d.ToProto()

	// Servers are not required to inline stdout and stderr, so we just set them to nil.
	// The client can just fetch them from the CAS if it needs them.
	actionResult.StdoutRaw = nil
	actionResult.StderrRaw = nil

	return nil
}

// buildNsjailArgs builds the arguments for nsjail.
func (e *Executor) buildNsjailArgs(sb *Sandbox, imageDir string, cmd *repb.Command) []string {
	// We use /mnt as the input root inside the sandbox. The exact path doesn't matter,
	// because all paths in REAPI are relative to the input root anyway, so the action
	// is relocatable and not tied to a specific input root.
	// TODO: Add support for overriding this via RBE's InputRootAbsolutePath feature?
	inputRoot := "/mnt"

	workDir := filepath.Join(inputRoot, cmd.WorkingDirectory)
	searchPath := []string{"/usr/local/sbin", "/usr/local/bin", "/usr/sbin", "/usr/bin", "/sbin", "/bin"}

	// If we're not using a container image, it means the action should be executed
	// on the host, so we need to set the image path to "/". Otherwise, nsjail will
	// run the command in a completely empty root filesystem, which will fail.
	if imageDir == "" {
		imageDir = "/"
	}

	args := []string{
		e.nsjailPath,
		"--quiet",
		"--chroot", imageDir,
		"--cwd", workDir,
		"--bindmount", fmt.Sprintf("%s:%s", sb.sandboxDir, inputRoot),
		"--env", "HOME=" + workDir,
		"--env", "PATH=" + strings.Join(searchPath, ":"),
	}

	// We don't need to set the values of the environment variables here because we
	// already set them in the environment of the nsjail process.
	for _, env := range cmd.EnvironmentVariables {
		args = append(args, "--env", env.Name)
	}

	// Provide a minimal /dev environment. We need to explicitly set the mode to 0755,
	// because otherwise nsjail sets the sticky bit on /dev, which causes "Permission denied"
	// errors when shells try to write into /dev/null.
	// See: https://github.com/lucidBrot/kctf-usage?tab=readme-ov-file#mount-devnull
	args = append(args, "--mount", "none:/dev:tmpfs:mode=0755")
	args = append(args, "--bindmount", "/dev/null")
	args = append(args, "--bindmount_ro", "/dev/zero")

	// Python and other tools use /dev/shm for shared memory, so we need to mount it.
	// Otherwise we get errors like this:
	// _multiprocessing.SemLock(kind, value, maxvalue)
	//   OSError: [Errno 38] Function not implemented
	args = append(args, "--tmpfsmount", "/dev/shm")

	// Some tools use /tmp for temporary files, so we need to mount it.
	// Otherwise we get errors like this:
	//   clang: error: unable to make temporary file: Read-only file system
	args = append(args, "--mount", "none:/tmp:tmpfs:size=2G")

	// nsjail applies rather strict resource limits by default, which can cause
	// some tools to fail. We disable them here for now, until we figure out
	// a set of limits that works for most tools.
	args = append(args, "--disable_rlimits")

	// nsjail doesn't look up argv[0] in PATH, so we need to resolve it ourselves
	// and pass it to nsjail via --exec_file.
	if !strings.ContainsAny(cmd.Arguments[0], "/") {
		for _, p := range searchPath {
			if _, err := os.Stat(filepath.Join(imageDir, p, cmd.Arguments[0])); err == nil {
				args = append(args, "--exec_file", filepath.Join(p, cmd.Arguments[0]))
				break
			}
		}
	}

	return append(args, "--")
}

// executeCommand runs cmd in the sandboxDir, which must already have been prepared by the caller.
// If we were able to execute the command, a valid ActionResult will be returned and error is nil.
// This includes the case where we ran the command, and it exited with an exit code != 0.
// However, if something went wrong during preparation or while spawning the process, an error is returned.
func (e *Executor) executeCommand(sb *Sandbox, action *repb.Action, cmd *repb.Command) (*repb.ActionResult, error) {
	var args []string

	imageURL := e.images.ImageURL(action, cmd)

	if e.nsjailPath != "" {
		// Prepare the container image for the action.
		imageDir, err := e.images.FetchImage(imageURL)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch container image: %w", err)
		}

		// If nsjail is available, we use it to sandbox the command.
		args = e.buildNsjailArgs(sb, imageDir, cmd)
	} else {
		// Otherwise, we just run the command directly on the host.
		// However, we can only do this if the action doesn't require a container image.
		if imageURL != "" {
			return nil, fmt.Errorf("action requires container image, but nsjail is not available")
		}
	}
	args = append(args, cmd.Arguments...)

	c := exec.Command(args[0], args[1:]...)
	c.Dir = filepath.Join(sb.sandboxDir, cmd.WorkingDirectory)

	for _, env := range cmd.EnvironmentVariables {
		c.Env = append(c.Env, fmt.Sprintf("%s=%s", env.Name, env.Value))
	}

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	if err := c.Run(); err != nil {
		// ExitError just means that the command returned a non-zero exit code.
		// In that case we just set the ExitCode in the ActionResult to it.
		// However, other errors mean that something went wrong, and we need to
		// return them to the caller.
		if exitErr := (&exec.ExitError{}); !errors.As(err, &exitErr) {
			return nil, err
		}
	}

	return &repb.ActionResult{
		ExitCode:  int32(c.ProcessState.ExitCode()),
		StdoutRaw: stdout.Bytes(),
		StderrRaw: stderr.Bytes(),
	}, nil
}

func (e *Executor) deleteSandbox(dir string) {
	// Walk through all directories inside the sandbox and give ourselves permission to delete
	// everything. This is necessary, because actions can create directories or files with wrong
	// permissions, causing os.RemoveAll to fail to delete them.
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			_ = os.Chmod(path, 0700)
		} else {
			// While we're here, attempt to remove the file. If it works, we save some
			// work later. If not, just ignore the error and continue, because
			// os.RemoveAll will take care of it.
			_ = os.Remove(path)
		}
		return nil
	})

	if err := os.RemoveAll(dir); err != nil {
		log.Printf("🚨 failed to remove sandbox: %v", err)
	}
}
