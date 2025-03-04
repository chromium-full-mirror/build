// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Bench is a simple benchmarking tool for Chromium builds.
package main

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// main is the entry point of our tool.
func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ERROR: %s\n", err)
		os.Exit(1)
	}

	os.Exit(0)
}

// run contains the main logic of the benchmarking tool.
func run() error {
	// Ensure we have a $HOME environment variable set, even on platforms that don't have it by
	// default, like Windows.
	if os.Getenv("HOME") == "" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("failed to get home directory: %w", err)
		}
		if err = os.Setenv("HOME", homeDir); err != nil {
			return fmt.Errorf("failed to set $HOME environment variable: %w", err)
		}
	}

	if err := parseFlags(); err != nil {
		return err
	}

	// Get the list of commits to benchmark at.
	commits, err := getCommits(*revision, *numCommits)
	if err != nil {
		return err
	}

	// Make any necessary changes to our environment variables.
	if err := setupEnvironment(); err != nil {
		return err
	}

	// Print some information about the active configuration.
	fmt.Fprintf(os.Stderr, "\nBenchmark configuration:\n")
	fmt.Fprintf(os.Stderr, "- Number of runs: %d\n", *runs)
	fmt.Fprintf(os.Stderr, "- Starting revision: %s\n", *revision)
	fmt.Fprintf(os.Stderr, "- Number of commits per run: %d\n", len(commits))
	if len(commits) > 1 {
		fmt.Fprintf(os.Stderr, "- Skip commits made by robots: %t\n", *skipRobots)
	}
	fmt.Fprintf(os.Stderr, "- Path to depot_tools: %s\n", *depotTools)
	fmt.Fprintf(os.Stderr, "- Path to chromium/src: %s\n", *workspaceRoot)
	fmt.Fprintf(os.Stderr, "- Path to results file: %s\n", *resultsPath)
	fmt.Fprintf(os.Stderr, "- Output directory: %s\n", *outputDir)
	fmt.Fprintf(os.Stderr, "- Targets: %s\n", strings.Join(targets, ", "))
	fmt.Fprintf(os.Stderr, "- Number of gn threads: %d\n", *gnThreads)
	fmt.Fprintf(os.Stderr, "- Always run 'gn gen': %t\n", *alwaysGnGen)
	fmt.Fprintf(os.Stderr, "- Always run 'gclient sync': %t\n", *alwaysSync)
	if *clean {
		fmt.Fprintf(os.Stderr, "- Build mode: Clean builds\n")
	} else if *clobber {
		fmt.Fprintf(os.Stderr, "- Build mode: Clobber builds\n")
	} else {
		fmt.Fprintf(os.Stderr, "- Build mode: Incremental builds\n")
	}

	fmt.Fprintf(os.Stderr, "\nBuild configuration (gn args):\n")
	fmt.Fprintf(os.Stderr, "- Using Siso: %t\n", *useSiso)
	fmt.Fprintf(os.Stderr, "- Using Remote Execution: %t\n", *useRemoteExec)
	fmt.Fprintf(os.Stderr, "- Using reclient: %t\n", *useReclient)

	// Print some interesting information about the system.
	fmt.Fprintf(os.Stderr, "\nSystem information:\n")
	fmt.Fprintf(os.Stderr, "- CPUs: %s\n", cpuInfo())
	fmt.Fprintf(os.Stderr, "- Memory: %s\n", memInfo())

	fmt.Fprintf(os.Stderr, "\nGetting launch approvals:\n")

	// Open the results file for writing.
	if *resultsPath == "" {
		results = csv.NewWriter(io.Discard)
	} else {
		fmt.Fprintf(os.Stderr, "- Opening results log ...\n")
		f, err := os.OpenFile(*resultsPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return fmt.Errorf("failed to open results file: %w", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to close results file, data might be incomplete: %v\n", err)
			}
		}()
		results = csv.NewWriter(f)
		defer func() {
			results.Flush()
			if err := results.Error(); err != nil {
				fmt.Fprintf(os.Stderr, "failed to flush results to disk, data might be incomplete: %v\n", err)
			}
		}()
	}

	// If we need to checkout a different commit, ensure that the workspace is clean and abort
	// if it is not. We cannot safely switch between commits in a dirty workspace.
	if !benchmarkCurrentCommitOnly() {
		fmt.Fprintf(os.Stderr, "- Checking if workspace is clean ...\n")
		isClean, err := checkWorkspaceClean()
		if err != nil {
			return err
		}
		if !isClean {
			return fmt.Errorf("workspace has pending changes, but we need to switch commits - please stash or commit your changes first")
		}
	}

	// Prepare the output directory for the benchmark.
	fmt.Fprintf(os.Stderr, "- Preparing the output directory ...\n")
	if err = prepareOutputDir(*outputDir); err != nil {
		return err
	}

	// Run the benchmark.
	for i := range *runs {
		fmt.Fprintf(os.Stderr, "\nBenchmark run %d/%d:\n", i+1, *runs)
		for _, commit := range commits {
			// Sync to the commit using "gclient sync", if necessary.
			if !benchmarkCurrentCommitOnly() {
				if err = syncToRevision(commit); err != nil {
					return err
				}
			}

			if *clean {
				// Clean the output directory using "gn clean".
				if err = cleanOutputDir(*outputDir); err != nil {
					return err
				}
			}

			// Run "gn gen" to generate the build files, if necessary.
			if *alwaysGnGen {
				if err = gnGen(*outputDir); err != nil {
					return err
				}
			}

			// Build the target using "autoninja".
			if err = autoNinja(); err != nil {
				return err
			}
		}
	}

	return nil
}

// benchmarkCurrentCommitOnly returns whether we should benchmark only the current commit.
// This allows us to skip a few validations and operations and allows the user to benchmark
// even pending changes in a dirty workspace.
func benchmarkCurrentCommitOnly() bool {
	return *numCommits == 1 && *revision == "HEAD"
}

// setupEnvironment sets up the necessary environment variables for the benchmark.
func setupEnvironment() error {
	// Unset any environment variables that might interfere with the benchmark.
	// We will set them ourselves if necessary. Ignore errors, as these variables might not be set.
	_ = os.Unsetenv("NINJA_STATUS")
	_ = os.Unsetenv("NINJA_SUMMARIZE_BUILD")
	_ = os.Unsetenv("RBE_exec_strategy")
	_ = os.Unsetenv("SISO_LIMITS")

	// Set UPDATE_DEPOT_TOOLS=0 in our environment to avoid updating depot_tools during the
	// benchmark. We take care to update it once before the benchmark starts.
	if err := os.Setenv("UPDATE_DEPOT_TOOLS", "0"); err != nil {
		return fmt.Errorf("failed to set $UPDATE_DEPOT_TOOLS: %w", err)
	}

	var sisoExperiments, sisoLimits []string

	// Set environment variables for the benchmark, if necessary.
	if *useReclient {
		if *depsCache {
			if err := os.Setenv("RBE_enable_deps_cache", "true"); err != nil {
				return fmt.Errorf("failed to set $RBE_enable_deps_cache: %w", err)
			}
		} else {
			if err := os.Setenv("RBE_enable_deps_cache", "false"); err != nil {
				return fmt.Errorf("failed to set $RBE_enable_deps_cache: %w", err)
			}
		}

		if *racing {
			if err := os.Setenv("RBE_exec_strategy", "racing"); err != nil {
				return fmt.Errorf("failed to set $RBE_exec_strategy: %w", err)
			}
		} else if *localFallback {
			if err := os.Setenv("RBE_exec_strategy", "remote_local_fallback"); err != nil {
				return fmt.Errorf("failed to set $RBE_exec_strategy: %w", err)
			}
		} else {
			if err := os.Setenv("RBE_exec_strategy", "remote"); err != nil {
				return fmt.Errorf("failed to set $RBE_exec_strategy: %w", err)
			}
		}
	}

	if *useSiso {
		if !*depsCache {
			sisoExperiments = append(sisoExperiments, "no-fast-deps")
		}

		if *racing && runtime.GOOS == "darwin" {
			// Warn the user that Siso does not use fastlocal by default on a Mac.
			fmt.Fprintf(os.Stderr,
				"Warning: Racing mode is enabled, but Siso's native RBE client "+
					"might not use it by default on a Mac.\n")
			fmt.Fprintf(os.Stderr,
				"If you want, you can manually override this by setting "+
					"$SISO_LIMITS to 'fastlocal=N',\nwhere N is the maximum "+
					"number of local jobs you want to run in parallel.\n")
		} else if !*racing {
			sisoLimits = append(sisoLimits, "fastlocal=0")
		}

		if !*localFallback {
			sisoExperiments = append(sisoExperiments, "no-local-fallback")
		}
	}

	if err := os.Setenv("SISO_EXPERIMENTS", strings.Join(sisoExperiments, ",")); err != nil {
		return err
	}
	if err := os.Setenv("SISO_LIMITS", strings.Join(sisoLimits, ",")); err != nil {
		return err
	}

	return nil
}

// checkWorkspaceClean checks if the workspace is clean and aborts if it is not.
func checkWorkspaceClean() (bool, error) {
	// Check if the workspace is clean.
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = *workspaceRoot
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return false, fmt.Errorf("failed to check if workspace is clean: %w", err)
	}
	if len(out) > 0 {
		return false, nil
	}

	return true, nil
}

// cleanOutputDir runs "gn clean" to clean the output directory.
func cleanOutputDir(outputDir string) error {
	fmt.Fprintf(os.Stderr, "- Cleaning the output directory ...\n")

	cmd := exec.Command("gn", "clean", outputDir)
	cmd.Dir = *workspaceRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to clean the output directory: %w", err)
	}
	if err := results.Write([]string{"clean", fmt.Sprintf("%f", time.Since(start).Seconds())}); err != nil {
		return fmt.Errorf("failed to write results: %w", err)
	}

	return nil
}

// syncToRevision runs "gclient sync" to sync to a specific commit, if necessary.
func syncToRevision(commit string) error {
	currentBranch, err := resolveBranch()
	if err != nil {
		return err
	}

	currentCommit, err := resolveCommit("HEAD")
	if err != nil {
		return err
	}

	// We need to switch to the commit using "git switch --detach" if either we're
	// currently on a branch (to avoid modifying it), or if we're not at the commit
	// we want to benchmark.
	if currentBranch != "" || currentCommit != commit {
		fmt.Fprintf(os.Stderr, "- Switching to commit %s ...\n", commit)
		cmd := exec.Command("git", "switch", "--detach", commit)
		cmd.Dir = *workspaceRoot
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		start := time.Now()
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to `git switch` to commit %q: %w", commit, err)
		}
		if err := results.Write([]string{"git_switch", fmt.Sprintf("%f", time.Since(start).Seconds())}); err != nil {
			return fmt.Errorf("failed to write results: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "- Already at commit %s, no need to switch.\n", commit)
	}

	// Sync dependencies and run hooks using `gclient sync` if necessary.
	if *alwaysSync || currentCommit != commit {
		fmt.Fprintf(os.Stderr, "- Running `gclient sync` ...\n")
		cmd := exec.Command(
			filepath.Join(*depotTools, "gclient"),
			"sync",
			"--delete_unversioned_trees",
			"--reset")
		cmd.Dir = *workspaceRoot
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		start := time.Now()
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to checkout commit %q: %w", commit, err)
		}
		if err := results.Write([]string{"gclient_sync", fmt.Sprintf("%f", time.Since(start).Seconds())}); err != nil {
			return fmt.Errorf("failed to write results: %w", err)
		}
	} else {
		fmt.Fprintf(os.Stderr, "- Already at commit %s, no need to run `gclient sync`.\n", commit)
	}

	return nil
}

// prepareOutputDir prepares the output directory for the benchmark.
func prepareOutputDir(outputDir string) error {
	// Ensure that the output directory exists.
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Write the GN args to outputDir/args.gn if they have changed.
	gnArgs := []string{
		"use_siso = " + strconv.FormatBool(*useSiso),
		"use_remoteexec = " + strconv.FormatBool(*useRemoteExec),
		"use_reclient = " + strconv.FormatBool(*useReclient),
	}
	argsFile := filepath.Join(outputDir, "args.gn")
	argsContents := []byte(strings.Join(gnArgs, "\n"))
	argsChanged, err := writeIfChanged(argsFile, argsContents)
	if err != nil {
		return fmt.Errorf("failed to write GN args to %q: %w", argsFile, err)
	}

	buildNinjaExists := true
	if _, err = os.Stat(filepath.Join(outputDir, "build.ninja")); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			buildNinjaExists = false
		} else {
			return fmt.Errorf("failed to stat build.ninja exists: %w", err)
		}
	}

	if argsChanged || !buildNinjaExists {
		return gnGen(outputDir)
	}

	return nil
}

// gnGen runs "gn gen" to generate the build files.
func gnGen(outputDir string) error {
	fmt.Fprintf(os.Stderr, "- Generating Ninja files ...\n")
	cmd := exec.Command("gn", "gen", outputDir, "--threads="+strconv.Itoa(*gnThreads))
	cmd.Dir = *workspaceRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to generate Ninja files: %w", err)
	}
	if err := results.Write([]string{"gn_gen", fmt.Sprintf("%f", time.Since(start).Seconds())}); err != nil {
		return fmt.Errorf("failed to write results: %w", err)
	}
	return nil
}

// autoNinja runs "autoninja" to build the target.
func autoNinja() error {
	fmt.Fprintf(os.Stderr, "- Building targets ...\n")

	autoninjaArgs := []string{
		"-C", *outputDir,
	}
	if *clobber {
		autoninjaArgs = append(autoninjaArgs, "-clobber")
	}
	autoninjaArgs = append(autoninjaArgs, targets...)
	autoninjaEnv := os.Environ()
	if !*useSiso {
		autoninjaEnv = append(autoninjaEnv, "NINJA_STATUS=[%r processes, %f/%t @ %o/s : %es ] ")
	}

	cmd := exec.Command(filepath.Join(*depotTools, "autoninja"), autoninjaArgs...)
	cmd.Dir = *workspaceRoot
	cmd.Env = autoninjaEnv
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	start := time.Now()
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build failed: %w", err)
	}
	if err := results.Write([]string{"build", fmt.Sprintf("%f", time.Since(start).Seconds())}); err != nil {
		return fmt.Errorf("failed to write results: %w", err)
	}

	return nil
}

// writeIfChanged writes the given content to the file, but only if the content has changed.
// It returns true if the file was written, false if it was not written, and an error if the
// operation failed.
func writeIfChanged(filename string, content []byte) (written bool, err error) {
	// Read the current content, if any.
	current, err := os.ReadFile(filename)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return false, fmt.Errorf("failed to read file %q: %w", filename, err)
	}

	// Check if the content has changed.
	if bytes.Equal(current, content) {
		return false, nil
	}

	// Write the new content.
	if err = os.WriteFile(filename, content, 0644); err != nil {
		return true, fmt.Errorf("failed to write file %q: %w", filename, err)
	}
	return true, nil
}
