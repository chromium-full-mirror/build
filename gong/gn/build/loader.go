// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package build

import (
	"fmt"
	"runtime"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

// Loader manages execution of the different build files. It receives
// requests when new references are found, and also manages loading the
// build config files.
type Loader struct {
	buildSettings    *BuildSettings
	inputFileManager *fs.InputFileManager

	// buildFileExtension is the additional extension for build files in this build.
	// The resulting file name will be "BUILD.<extension>.gn".
	buildFileExtension string

	// Set of build files that have already been requested for load.
	seen map[loadID]struct{}

	// Metadata for each toolchain.
	toolchains map[Label]*toolchainRecord
}

// loadID represents a tuple of a file and toolchain label for tracking loaded files.
type loadID struct {
	file      fs.SourceFile
	toolchain Label
}

type toolchainRecord struct {
	settings *Settings
	// Whether the global build config has been loaded for this toolchain instance.
	// Once the build config is loaded, we can start loading other files.
	configLoaded     bool
	waitingForConfig []fs.SourceFile
}

func newToolchainRecord(loader *Loader) *toolchainRecord {
	// The default toolchain label can be empty for the first time the default
	// toolchain is loaded, since we don't know it yet. This will be fixed up
	// later. It should be valid in all other cases.
	return &toolchainRecord{
		settings:     NewSettings(loader.buildSettings),
		configLoaded: false,
	}
}

// MakeLoader creates a loader.
func MakeLoader(buildSettings *BuildSettings, inputFileManager *fs.InputFileManager) Loader {
	return Loader{
		buildSettings:    buildSettings,
		inputFileManager: inputFileManager,
		seen:             make(map[loadID]struct{}),
		toolchains:       make(map[Label]*toolchainRecord),
	}
}

func (l *Loader) buildFileForLabel(label Label) (fs.SourceFile, error) {
	return fs.MakeSourceFile(label.dir + "BUILD" + l.buildFileExtension + ".gn")
}

// Load schedules a file load, noting down where the load came from.
// If intoToolchain is the zero value, the default toolchain will be used.
func (l *Loader) Load(file fs.SourceFile, origin syntax.LocationRange, intoToolchain Label) error {
	loadID := loadID{
		file:      file,
		toolchain: intoToolchain,
	}
	if _, ok := l.seen[loadID]; ok {
		// Already seen, so this file was already loaded or scheduled for load.
		return nil
	}
	l.seen[loadID] = struct{}{}

	if len(l.toolchains) == 0 {
		// Nothing loaded, need to load the default build config. The initial load
		// should not specify a toolchain.
		if intoToolchain != (Label{}) {
			return fmt.Errorf("can't load into toolchain %q before default build config is loaded", intoToolchain)
		}
		record := newToolchainRecord(l)
		l.toolchains[Label{}] = record

		record.waitingForConfig = append(record.waitingForConfig, file)
		if err := l.loadBuildConfig(record.settings); err != nil {
			return err
		}

		return nil
	}

	// TODO: implement.
	return fmt.Errorf("loading files after the first one not implemented yet. requested: %q", file.Filename())
}

func (l *Loader) loadBuildConfig(settings *Settings) error {
	// TODO: run the load asynchronously in the background.
	root, err := l.inputFileManager.LoadFile(syntax.LocationRange{}, l.buildSettings, l.buildSettings.BuildConfigFile)
	if err != nil {
		return fmt.Errorf("failed to load buildconfig: %w", err)
	}

	// Hack to populate os/arch until we properly implement build-level args.
	// TODO: support os/arch overrides.
	switch os := runtime.GOOS; os {
	case "windows":
		settings.baseConfig.SetValue("host_os", resolve.NewOriginlessStringValue("win"), nil)
	case "linux":
		settings.baseConfig.SetValue("host_os", resolve.NewOriginlessStringValue("linux"), nil)
	case "darwin":
		settings.baseConfig.SetValue("host_os", resolve.NewOriginlessStringValue("mac"), nil)
	default:
		return fmt.Errorf("OS not handled. (%q)", os)
	}
	settings.baseConfig.SetValue("target_os", resolve.NewOriginlessStringValue(""), nil)
	settings.baseConfig.SetValue("current_os", resolve.NewOriginlessStringValue(""), nil)
	switch arch := runtime.GOARCH; arch {
	case "amd64":
		settings.baseConfig.SetValue("host_cpu", resolve.NewOriginlessStringValue("x64"), nil)
	case "arm64":
		settings.baseConfig.SetValue("host_cpu", resolve.NewOriginlessStringValue("arm64"), nil)
	default:
		return fmt.Errorf("OS architecture not handled. (%q)", arch)
	}
	settings.baseConfig.SetValue("target_cpu", resolve.NewOriginlessStringValue(""), nil)
	settings.baseConfig.SetValue("current_cpu", resolve.NewOriginlessStringValue(""), nil)

	_, err = resolve.ExecuteNode(root, settings.baseConfig)
	if err != nil {
		return fmt.Errorf("failed to execute buildconfig: %w", err)
	}

	return nil
}
