// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execution

import (
	"archive/tar"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	repb "github.com/bazelbuild/remote-apis/build/bazel/remote/execution/v2"
	"golang.org/x/sync/singleflight"
)

// ImageRepository represents a repository for managing container images.
// It uses Docker to fetch and extract container images for actions.
type ImageRepository struct {
	// Base directory for all images
	baseDir string

	// Path to the `docker` executable.
	dockerPath string

	// Synchronization mechanism to prevent multiple concurrent downloads of the same image.
	imageGroup singleflight.Group
}

// NewImageRepository creates a new image repository with the given base directory.
func NewImageRepository(baseDir string) (*ImageRepository, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("baseDir must not be empty")
	}

	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create directory %q: %w", baseDir, err)
	}

	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return nil, fmt.Errorf("failed to find docker executable: %w", err)
	}

	return &ImageRepository{
		baseDir:    baseDir,
		dockerPath: dockerPath,
	}, nil
}

// ImageURL returns the container image to use for the given action.
func (r *ImageRepository) ImageURL(action *repb.Action, cmd *repb.Command) string {
	// REAPI v2.2+ clients set the container image in the action itself.
	platform := action.Platform
	if platform == nil {
		// REAPI v2.1 and earlier clients set the container image in the command.
		platform = cmd.Platform //nolint:staticcheck
	}
	if platform != nil {
		for _, prop := range platform.Properties {
			if prop.Name == "container-image" {
				return prop.Value
			}
		}
	}
	return ""
}

// FetchImage fetches the container image for the given action and extracts it into the image directory.
// It returns the path to the extracted image.
func (r *ImageRepository) FetchImage(containerImage string) (string, error) {
	// Verify that the image URL starts with "docker://" and strip it.
	if !strings.HasPrefix(containerImage, "docker://") {
		return "", fmt.Errorf("container image URL %q must start with docker://", containerImage)
	}
	containerImage = strings.TrimPrefix(containerImage, "docker://")

	// Ensure that the image URL has a valid @sha256 suffix and extract it.
	if !strings.Contains(containerImage, "@sha256:") {
		return "", fmt.Errorf("container image URL %q must have a @sha256 suffix", containerImage)
	}
	containerHash := containerImage[strings.LastIndex(containerImage, "@sha256:")+len("@sha256:"):]
	if h, err := hex.DecodeString(containerHash); err != nil || len(h) != 32 {
		return "", fmt.Errorf("container image hash %q must be a valid SHA256 hash", containerHash)
	}

	// Fetch the image if we don't have it yet.
	imagePath, err, _ := r.imageGroup.Do(containerHash, func() (interface{}, error) {
		// Check if we already have the image.
		imagePath := filepath.Join(r.baseDir, containerHash)
		if _, err := os.Stat(imagePath); err == nil {
			return imagePath, nil
		}

		// Create a container for the image required by the action.
		c := exec.Command(r.dockerPath, "create", containerImage)
		out, err := c.Output()
		if err != nil {
			// Get the error output from the error by casting it to *exec.ExitError.
			var ee *exec.ExitError
			if errors.As(err, &ee) {
				return "", fmt.Errorf("failed to create container: %w: %s", err, ee.Stderr)
			}
			return "", fmt.Errorf("failed to create container: %w", err)
		}
		containerName := strings.TrimSpace(string(out))

		// Delete the container when we're done.
		defer func() {
			c := exec.Command(r.dockerPath, "rm", containerName)
			if _, err := c.Output(); err != nil {
				var ee *exec.ExitError
				if errors.As(err, &ee) {
					log.Printf("🚨 failed to remove container: %v: %s", err, ee.Stderr)
				} else {
					log.Printf("🚨 failed to remove container: %v", err)
				}
			}
		}()

		// Export the container and extracts the generated tar archive into to a
		// subdirectory of our image directory using Go's archive/tar package.
		tmpImagePath, err := os.MkdirTemp(r.baseDir, containerHash+".tmp-*")
		if err != nil {
			return "", fmt.Errorf("failed to create temporary image directory: %w", err)
		}
		defer func() {
			if err := os.RemoveAll(tmpImagePath); err != nil {
				log.Printf("🚨 failed to remove temporary image directory: %v", err)
			}
		}()

		c = exec.Command(r.dockerPath, "export", containerName)
		stdout, err := c.StdoutPipe()
		if err != nil {
			return "", fmt.Errorf("failed to get stdout pipe: %w", err)
		}
		if err := c.Start(); err != nil {
			return "", fmt.Errorf("failed to start command: %w", err)
		}
		if err := extractTar(stdout, tmpImagePath); err != nil {
			return "", fmt.Errorf("failed to extract tar archive: %w", err)
		}
		if err := c.Wait(); err != nil {
			return "", fmt.Errorf("failed to wait for command: %w", err)
		}

		// If we got here, we successfully extracted the image, so we can move it to its
		// final location.
		if err := os.Rename(tmpImagePath, imagePath); err != nil {
			return "", fmt.Errorf("failed to rename image directory: %w", err)
		}

		return imagePath, nil
	})
	if err != nil {
		return "", err
	}
	return imagePath.(string), nil
}

// extractTar extracts the given tar archive into the given directory.
func extractTar(archive io.ReadCloser, destDir string) error {
	defer func() {
		// Safe to ignore errors here, because we're just reading from the archive.
		_ = archive.Close()
	}()

	// Create a new tar reader.
	tr := tar.NewReader(archive)

	// Extract each file in the archive.
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("failed to read tar header: %w", err)
		}

		// Ensure that the file is not outside the destination directory.
		if !filepath.IsLocal(hdr.Name) {
			return fmt.Errorf("tar file %q points outside of destination directory", hdr.Name)
		}

		// Create the right type of file.
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(filepath.Join(destDir, hdr.Name), 0755); err != nil {
				return fmt.Errorf("failed to create directory: %w", err)
			}
		case tar.TypeReg:
			f, err := os.Create(filepath.Join(destDir, hdr.Name))
			if err != nil {
				return fmt.Errorf("failed to create file: %w", err)
			}
			if _, err := io.Copy(f, tr); err != nil {
				return fmt.Errorf("failed to copy file contents: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("failed to close file: %w", err)
			}
		case tar.TypeLink:
			if !filepath.IsLocal(hdr.Linkname) {
				return fmt.Errorf("hardlink %q -> %q in tar file points outside of destination directory", hdr.Name, hdr.Linkname)
			}
			if err := os.Link(filepath.Join(destDir, hdr.Linkname), filepath.Join(destDir, hdr.Name)); err != nil {
				return fmt.Errorf("failed to create hard link: %w", err)
			}
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, filepath.Join(destDir, hdr.Name)); err != nil {
				return fmt.Errorf("failed to create symlink: %w", err)
			}
		default:
			return fmt.Errorf("unknown tar header type: %v", hdr.Typeflag)
		}

		// If the file has a mode, set it.
		if hdr.Mode != 0 && hdr.Typeflag != tar.TypeSymlink {
			if err := os.Chmod(filepath.Join(destDir, hdr.Name), os.FileMode(hdr.Mode)); err != nil {
				return fmt.Errorf("failed to set mode: %w", err)
			}
		}

		// If the file has a mtime, set it.
		if !hdr.ModTime.IsZero() && hdr.Typeflag != tar.TypeSymlink {
			if err := os.Chtimes(filepath.Join(destDir, hdr.Name), hdr.ModTime, hdr.ModTime); err != nil {
				return fmt.Errorf("failed to set mtime: %w", err)
			}
		}
	}

	return nil
}
