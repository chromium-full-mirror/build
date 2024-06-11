// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func createFakeGitRepoForTesting(t *testing.T, dir string) []string {
	t.Helper()

	// Create a fake git repo for testing.
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("Failed to create directory %s: %v", dir, err)
	}

	// Create the Git repository.
	cmd := exec.Command("git", "init", "--initial-branch=main")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("Failed to initialize git repo: %v", err)
	}

	// Generate 10 commits.
	var commits []string
	for i := 0; i < 10; i++ {
		// Random file name and content
		fileName := fmt.Sprintf("file%d.txt", i)
		fileContent := []byte(fmt.Sprintf("This is commit number %d.", i))
		if err := os.WriteFile(filepath.Join(dir, fileName), fileContent, 0644); err != nil {
			t.Fatalf("Failed to write file %s: %v", fileName, err)
		}

		// Generate a nice mix of "human" and "robot" authors.
		authorName := fmt.Sprintf("Author %d", i)
		var authorEmail string
		switch i {
		case 4:
			authorEmail = "chromium-rickroll@skia-public.iam.gserviceaccount.com"
		case 5:
			authorEmail = "mdb.chrome-pki-metrics-release-jobs@google.com"
		case 6:
			authorEmail = "chrome-metadata-team+robot@google.com"
		case 7:
			authorEmail = "congratbot@prod.google.com"
		default:
			authorEmail = fmt.Sprintf("author%d@example.com", i)
		}

		// Add and commit.
		cmd = exec.Command("git", "add", fileName)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to add file %s: %v", fileName, err)
		}

		cmd = exec.Command(
			"git", "commit",
			"-m", fmt.Sprintf("Commit %d", i),
			"--author", fmt.Sprintf("%s <%s>", authorName, authorEmail))
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			t.Fatalf("Failed to commit file %s: %v", fileName, err)
		}

		// Save the commit hash.
		cmd = exec.Command("git", "rev-parse", "HEAD")
		cmd.Stderr = os.Stderr
		hash, err := cmd.Output()
		if err != nil {
			t.Fatalf("Failed to get commit hash: %v", err)
		}
		commits = append(commits, strings.TrimSpace(string(hash)))
	}

	return commits
}

func TestGetCommits(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatalf("Failed to create directory %s: %v", repoDir, err)
	}
	// Change to the repo directory and change back to the original directory when done.
	// TODO: go.dev/issue/62516 - Use t.Chdir when it's available.
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Failed to get current working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(oldCwd)
	})
	if err = os.Chdir(repoDir); err != nil {
		t.Fatalf("Failed to change to directory %s: %v", repoDir, err)
	}

	// Don't load the system's or user's git configuration, as it may interfere with the test.
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_SYSTEM", "/dev/null")
	t.Setenv("GIT_COMMITTER_NAME", "Test User")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")

	repoCommits := createFakeGitRepoForTesting(t, repoDir)

	tests := map[string]struct {
		startRev    string
		wantCommits []string
		wantError   error
	}{
		"invalid_numcommits": {
			startRev:  "HEAD",
			wantError: errInvalidNumCommits,
		},
		"just_head": {
			startRev:    "HEAD",
			wantCommits: []string{repoCommits[9]},
		},
		"filter_robot_commits": {
			startRev:    "HEAD",
			wantCommits: slices.Concat(repoCommits[0:4], repoCommits[8:10]),
		},
		"startrev_in_middle": {
			startRev:    repoCommits[5],
			wantCommits: repoCommits[1:4],
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			commits, err := getCommits(tc.startRev, len(tc.wantCommits))
			if err != nil && !errors.Is(err, tc.wantError) {
				t.Fatalf("getCommits returned an unexpected error: %v", err)
			}
			if tc.wantError != nil {
				return
			}

			if diff := cmp.Diff(tc.wantCommits, commits); diff != "" {
				t.Fatalf("getCommits returned unexpected commits (-want +got):\n%s", diff)
			}
		})
	}
}
