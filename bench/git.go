// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

var (
	errInvalidNumCommits = errors.New("invalid number of commits")
)

// resolveBranch returns the name of the current branch, or an empty string if
// the current workspace is in a detached HEAD state.
func resolveBranch() (string, error) {
	cmd := exec.Command("git", "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve branch: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// resolveCommit resolves the given revision to a hash.
func resolveCommit(revision string) (string, error) {
	cmd := exec.Command("git", "rev-parse", revision)
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve revision %q: %w", revision, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// isRobotAuthor returns true if the given author email is a robot.
func isRobotAuthor(authorEmail string) bool {
	// Example: chromium-autoroll@skia-public.iam.gserviceaccount.com
	if strings.HasSuffix(authorEmail, ".gserviceaccount.com") {
		return true
	}
	// Example: mdb.chrome-pki-metadata-release-jobs@google.com
	if strings.HasPrefix(authorEmail, "mdb.") && strings.HasSuffix(authorEmail, "@google.com") {
		return true
	}
	// Example: chrome-metrics-team+robot@google.com
	if strings.HasSuffix(authorEmail, "+robot@google.com") {
		return true
	}
	// Example: chops-security-borg@prod.google.com
	if strings.HasSuffix(authorEmail, "@prod.google.com") {
		return true
	}
	return false
}

// getCommits returns a list of commits to benchmark at.
func getCommits(startRev string, numCommits int) ([]string, error) {
	// Return early for the trivial cases.
	if numCommits < 1 {
		return nil, errInvalidNumCommits
	} else if numCommits == 1 {
		commit, err := resolveCommit(startRev)
		if err != nil {
			return nil, err
		}
		return []string{commit}, nil
	}

	// Use git log to get a stream of "<hash> <author-email>" lines for the
	// commit history beginning at startRev.
	cmd := exec.Command("git", "log", "--format=%H %ae", startRev)
	cmd.Stderr = os.Stderr
	cmdReader, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("failed to create stdout pipe: %w", err)
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start git log: %w", err)
	}

	// Parse git log's output and collect the commits in a slice.
	commits := make([]string, 0, numCommits)
	skipped := 0
	scanner := bufio.NewScanner(cmdReader)
	for scanner.Scan() {
		logLine := scanner.Text()
		hash, authorEmail, ok := strings.Cut(logLine, " ")
		if !ok {
			return nil, fmt.Errorf("failed to parse commit %q", logLine)
		}
		if *skipRobots && isRobotAuthor(authorEmail) {
			skipped++
			continue
		}
		commits = append(commits, hash)
		if len(commits) == numCommits {
			// We have enough commits, stop reading from the pipe.
			if err = cmdReader.Close(); err != nil {
				return nil, fmt.Errorf("failed to close stdout pipe: %w", err)
			}
			break
		}
	}
	if err = scanner.Err(); err != nil {
		return nil, fmt.Errorf("failed to read git log output: %w", err)
	}

	// Sort the commits in reverse order, so that the oldest commit is first.
	// When we benchmark incremental builds, we want to start with the oldest
	// commit and work our way up to the newest commit, like a normal developer
	// would do during their work.
	fmt.Fprintf(os.Stderr, "Found %d commits to benchmark, skipped %d robot commits.\n", len(commits), skipped)
	slices.Reverse(commits)

	// Wait for git to exit.
	if err = cmd.Wait(); err != nil {
		// If git log exited with a non-zero status, check if we got the expected
		// number of commits. If yes, we can ignore the error (it's probably just
		// because we closed the pipe early). If not, it's a real error.
		if len(commits) != numCommits {
			return nil, fmt.Errorf("failed to wait for git to exit: %w", err)
		}
	}

	return commits, nil
}
