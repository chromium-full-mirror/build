// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"

	"go.chromium.org/build/kajiya/blobstore"
	"go.chromium.org/build/kajiya/execution/model"
)

// Executor is a local executor that executes actions on the local machine.
// It uses a ContentAddressableStorage to fetch all required inputs for the action
// into a sandbox directory, executes the action in that sandbox directory, and
// then uploads the output files and directories to the CAS after the action has finished.
type Executor struct {
	cas             *blobstore.ContentAddressableStorage
	images          *ImageRepository
	nsjailPath      string
	sandboxBase     string
	sandboxStrategy SandboxStrategy
	trees           *TreeRepository
	allowHostFS     bool
	traceInputs     bool

	// FuseFS-specific machinery. Only set when sandboxStrategy == FuseFS.
	// Lifecycle (mount + register/unregister + unmount) is owned entirely
	// by this struct so non-FUSE strategies can leave it nil.
	fuse *fuseBackend
}

// New creates a new Executor.
func New(baseDir string, cas *blobstore.ContentAddressableStorage, sb SandboxStrategy, allowHostFS bool, traceInputs bool) (*Executor, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir must be set")
	}

	if cas == nil {
		return nil, fmt.Errorf("cas must be set")
	}

	// Create the data directory if it doesn't exist.
	err := os.Mkdir(baseDir, 0755)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("failed to create directory %q: %w", baseDir, err)
	}

	// Create the image repository.
	images, err := NewImageRepository(filepath.Join(baseDir, "images"))
	if err != nil {
		return nil, fmt.Errorf("failed to create image repository: %w", err)
	}

	// Remove any existing sandboxes that might have been left over from a previous run.
	sandboxBase := filepath.Join(baseDir, "tmp")
	if dirs, err := os.ReadDir(sandboxBase); err == nil || errors.Is(err, fs.ErrNotExist) {
		for _, d := range dirs {
			deleteSandbox(filepath.Join(sandboxBase, d.Name()))
		}
		if len(dirs) > 0 {
			slog.Info("removed leftover sandboxes", "count", len(dirs))
		}
	} else {
		return nil, fmt.Errorf("failed to read sandbox base directory: %w", err)
	}

	// Create the base directory for sandboxes.
	if err := os.Mkdir(sandboxBase, 0755); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, fmt.Errorf("failed to create sandbox base %q: %w", sandboxBase, err)
	}

	// Create the tree repository. It's only used for nested overlay filesystems.
	var trees *TreeRepository
	treeRoot := filepath.Join(baseDir, "trees")
	if sb == NestedOverlayFS {
		trees, err = newTreeRepository(treeRoot, cas)
		if err != nil {
			return nil, fmt.Errorf("failed to create tree repository: %w", err)
		}
	} else {
		if err := os.RemoveAll(treeRoot); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("failed to remove tree repository: %w", err)
		}
	}

	// Try to find nsjail in the PATH.
	nsjailPath := ""
	if runtime.GOOS == "linux" {
		nsjailPath, err = exec.LookPath("nsjail")
		if err != nil {
			return nil, fmt.Errorf("🚨 required tool 'nsjail' not found in PATH")
		}
	}

	// Mount the CAS-backed FUSE filesystem. Only the FuseFS strategy uses
	// it; other strategies clean up any leftover mount/mountpoint from a
	// previous FuseFS run so it doesn't accumulate.
	fuseMountpoint := filepath.Join(baseDir, "fuse")
	var fuseBE *fuseBackend
	if sb == FuseFS {
		fuseBE, err = newFuseBackend(fuseMountpoint)
		if err != nil {
			return nil, err
		}
	} else {
		cleanFuseMountpoint(fuseMountpoint)
	}

	if traceInputs && sb != FuseFS {
		slog.Warn("--trace_inputs is only supported with the FuseFS sandbox strategy; ignoring")
		traceInputs = false
	}

	return &Executor{
		cas:             cas,
		images:          images,
		nsjailPath:      nsjailPath,
		sandboxBase:     sandboxBase,
		sandboxStrategy: sb,
		traceInputs:     traceInputs,
		trees:           trees,
		allowHostFS:     allowHostFS,
		fuse:            fuseBE,
	}, nil
}

// Close cleans up resources held by the Executor. Must be called on shutdown.
func (e *Executor) Close() error {
	return e.fuse.Close()
}

// Execute executes the given action and returns the result.
func (e *Executor) Execute(action *model.Action) (*repb.ActionResult, error) {
	// Build a sandbox directory for the action.
	sandboxDir, err := os.MkdirTemp(e.sandboxBase, "*")
	if err != nil {
		return nil, fmt.Errorf("failed to create sandbox directory: %w", err)
	}
	defer func() {
		// Deregister the FUSE sandbox before deleting the sandbox directory,
		// so the kernel doesn't try to access inodes that are being removed.
		if e.fuse != nil {
			e.fuse.UnregisterSandbox(filepath.Base(sandboxDir))
		}
		deleteSandbox(sandboxDir)
	}()

	var recorder *AccessRecorder
	if e.traceInputs {
		recorder = NewAccessRecorder()
	}

	sb := &Sandbox{
		cas:        e.cas,
		trees:      e.trees,
		digestFn:   action.Fn,
		sandboxDir: sandboxDir,
		strategy:   e.sandboxStrategy,
		fuse:       e.fuse,
		recorder:   recorder,
	}

	// Stage the input files and directories into the sandbox.
	if err := sb.Prepare(action); err != nil {
		return nil, fmt.Errorf("failed to prepare input root: %w", err)
	}

	// Execute the command.
	actionResult, err := e.executeCommand(sb, action)
	if err != nil {
		return nil, fmt.Errorf("failed to execute command: %w", err)
	}

	// Save stdout and stderr to the CAS and update their digests in the action result.
	if err := e.saveStdOutErr(action.Fn, actionResult); err != nil {
		return nil, err
	}

	// Go through all output files and directories and upload them to the CAS.
	if err := sb.UploadOutputs(action, actionResult); err != nil {
		return nil, fmt.Errorf("failed to upload outputs: %w", err)
	}

	// Attach observed input paths to the action result as auxiliary metadata.
	if recorder != nil {
		if err := attachObservedInputs(actionResult, recorder); err != nil {
			return nil, fmt.Errorf("failed to attach observed inputs: %w", err)
		}
	}

	return actionResult, nil
}

// saveStdOutErr saves stdout and stderr to the CAS and returns the updated action result.
func (e *Executor) saveStdOutErr(fn digest.Function, actionResult *repb.ActionResult) error {
	d, err := e.cas.Put(fn, actionResult.StdoutRaw)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to put stdout into CAS: %v", err)
	}
	actionResult.StdoutDigest = d.Proto()

	d, err = e.cas.Put(fn, actionResult.StderrRaw)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to put stderr into CAS: %v", err)
	}
	actionResult.StderrDigest = d.Proto()

	// Servers are not required to inline stdout and stderr, so we just set them to nil.
	// The client can just fetch them from the CAS if it needs them.
	actionResult.StdoutRaw = nil
	actionResult.StderrRaw = nil

	return nil
}

// buildNsjailArgs builds the arguments for nsjail.
func (e *Executor) buildNsjailArgs(sb *Sandbox, imageDir string, action *model.Action) []string {
	// We use /mnt as the input root inside the sandbox. The exact path doesn't matter,
	// because all paths in REAPI are relative to the input root anyway, so the action
	// is relocatable and not tied to a specific input root.
	// TODO: Add support for overriding this via RBE's InputRootAbsolutePath feature?
	inputRoot := "/mnt"

	workDir := filepath.Join(inputRoot, action.WorkingDir)
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
		"--env", "HOME=" + workDir,
		"--env", "PATH=" + strings.Join(searchPath, ":"),
	}

	// Add any extra args required by the sandbox.
	args = append(args, sb.extraNsjailArgs...)

	// We don't need to set the values of the environment variables here because we
	// already set them in the environment of the nsjail process.
	for _, v := range action.EnvVars {
		args = append(args, "--env", v.Name)
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
	if !strings.ContainsAny(action.Args[0], "/") {
		for _, p := range searchPath {
			if _, err := os.Stat(filepath.Join(imageDir, p, action.Args[0])); err == nil {
				args = append(args, "--exec_file", filepath.Join(p, action.Args[0]))
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
func (e *Executor) executeCommand(sb *Sandbox, action *model.Action) (*repb.ActionResult, error) {
	if action.ContainerImage == "" && !e.allowHostFS {
		return nil, fmt.Errorf("action has no container image and --allow_host_fs is not set; refusing to expose host filesystem")
	}
	if action.ContainerImage != "" && e.nsjailPath == "" {
		return nil, fmt.Errorf("action requires container image, but nsjail is not available")
	}

	var args []string

	if e.nsjailPath != "" {
		// Prepare the container image for the action, if one is specified.
		var imageDir string
		if action.ContainerImage != "" {
			var err error
			imageDir, err = e.images.FetchImage(action.ContainerImage)
			if err != nil {
				return nil, fmt.Errorf("failed to fetch container image: %w", err)
			}
		}

		// If nsjail is available, we use it to sandbox the command.
		args = e.buildNsjailArgs(sb, imageDir, action)
	}

	args = append(args, action.Args...)

	c := exec.Command(args[0], args[1:]...)

	// If we're using nsjail, the working directory doesn't really matter, because nsjail will
	// change to the correct working directory inside the sandbox anyway. However, if we're not
	// using nsjail, we need to set the working directory to the sandbox directory.
	c.Dir = sb.sandboxDir
	if e.nsjailPath == "" {
		c.Dir = filepath.Join(c.Dir, action.WorkingDir)
	}

	for _, v := range action.EnvVars {
		c.Env = append(c.Env, fmt.Sprintf("%s=%s", v.Name, v.Value))
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

func deleteSandbox(dir string) {
	// The "work" directory is a special case, because it is used by the kernel as a temporary
	// scratch space for overlayfs. It will usually be empty or only contain very few
	// directories or marker files without any permission bits set. We need to reset them so
	// that os.RemoveAll() below can successfully delete this dir.
	_ = filepath.WalkDir(filepath.Join(dir, "work"), func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			_ = os.Chmod(path, 0700)
		}
		return nil
	})

	// First, try to delete the sandbox via os.RemoveAll. This works in most cases and is the
	// fastest way, using the least amount of CPU and syscalls.
	if err := os.RemoveAll(dir); err == nil {
		return
	}

	// If that didn't work, the action probably created a directory with restrictive
	// permissions, so we need to walk through all directories and make them writable.
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() {
			fi, err := d.Info()
			if err != nil {
				slog.Error("failed to get file info", "path", path, "error", err)
				return nil
			}
			mode := fi.Mode()
			if mode&0200 == 0 {
				err = os.Chmod(path, mode|0200)
				if err != nil {
					slog.Error("failed to chmod", "path", path, "error", err)
				}
			}
		}
		return nil
	})
	if err := os.RemoveAll(dir); err != nil {
		slog.Error("failed to remove sandbox", "path", dir, "error", err)
	}
}
