// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"errors"
	"fmt"
	"runtime"

	"go.chromium.org/build/gong/gn/build/environment"
	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/build/graph"
	"go.chromium.org/build/gong/gn/resolve"
	"go.chromium.org/build/gong/gn/syntax"
)

// ErrToolchainNotReady is returned when attempted load into non-ready toolchain.
var ErrToolchainNotReady = errors.New("toolchain not ready")

// Loader manages execution of the different build files. It receives
// requests when new references are found, and also manages loading the
// build config files.
type Loader struct {
	buildSettings    *environment.BuildSettings
	inputFileManager *fs.InputFileManager

	// BuildFileExtension is the additional extension for build files in this build.
	// The resulting file name will be "BUILD.<extension>.gn".
	BuildFileExtension string

	// Set of build files that have already been requested for load.
	seen map[loadID]struct{}

	// Metadata for each toolchain.
	toolchains map[environment.Label]*toolchainRecord

	// Label for the default toolchain.
	defaultToolchain environment.Label
}

// loadID represents a tuple of a file and toolchain label for tracking loaded files.
type loadID struct {
	file      fs.SourceFile
	toolchain environment.Label
}

type toolchainRecord struct {
	settings *Settings
	// Whether the global build config has been loaded for this toolchain instance.
	// Once the build config is loaded, we can start loading other files.
	configLoaded bool
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
func MakeLoader(buildSettings *environment.BuildSettings, inputFileManager *fs.InputFileManager) Loader {
	return Loader{
		buildSettings:    buildSettings,
		inputFileManager: inputFileManager,
		seen:             make(map[loadID]struct{}),
		toolchains:       make(map[environment.Label]*toolchainRecord),
	}
}

// BuildFileForLabel returns the build file that the given label references.
func (l *Loader) BuildFileForLabel(label environment.Label) (fs.SourceFile, error) {
	return fs.MakeSourceFile(label.Dir.Path() + "BUILD" + l.BuildFileExtension + ".gn")
}

// Load attempts to load and execute a buildfile, noting down where the load came from.
// If intoToolchain is the zero value, the default toolchain will be used.
//
// For initial prototyping purposes, this will run synchronously and will not be threadsafe.
func (l *Loader) Load(file fs.SourceFile, origin syntax.LocationRange, intoToolchain environment.Label) ([]graph.Item, error) {
	loadID := loadID{
		file:      file,
		toolchain: intoToolchain,
	}
	if _, ok := l.seen[loadID]; ok {
		// Already seen, so this file was already loaded or scheduled for load.
		return nil, nil
	}
	l.seen[loadID] = struct{}{}

	if intoToolchain == (environment.Label{}) {
		intoToolchain = l.defaultToolchain
	}

	record, ok := l.toolchains[intoToolchain]
	if !ok {
		record = newToolchainRecord(l)
		if len(l.toolchains) == 0 {
			if intoToolchain != (environment.Label{}) {
				return nil, fmt.Errorf("can't load into toolchain %q before default build config is loaded", intoToolchain.UserVisibleString(false))
			}
			if err := l.loadBuildConfig(record.settings); err != nil {
				return nil, err
			}
			intoToolchain = l.defaultToolchain
		} else {
			if err := l.loadBuildConfig(record.settings); err != nil {
				return nil, err
			}
		}
		record.configLoaded = true
		l.toolchains[intoToolchain] = record
	}

	if !record.configLoaded {
		return nil, ErrToolchainNotReady
	}
	return l.loadFile(file, record.settings)
}

// loadBuildConfig loads the build config for the provided toolchain settings object.
func (l *Loader) loadBuildConfig(settings *Settings) error {
	baseContext, err := contextFromScope(settings.baseConfig)
	if err != nil {
		return err
	}

	baseContext.processingBuildConfig = true
	defer func() {
		baseContext.processingBuildConfig = false
	}()

	// If the default toolchain label is unknown, the default toolchain is being processed.
	// Set the receiver to populate it.
	// (It can be called an unlimited number of times, the last call wins.)
	if l.defaultToolchain == (environment.Label{}) {
		baseContext.defaultToolchainReceiver = func(toolchainLabel environment.Label) {
			l.defaultToolchain = toolchainLabel
		}
	}

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

	if baseContext.defaultToolchainReceiver != nil {
		// The default toolchain must have been set in the default build config file.
		if l.defaultToolchain == (environment.Label{}) {
			return environment.BuildConfigError{
				Msg: "The default build config file did not call set_default_toolchain()",
				Help: `If you don't call this, I can't figure out what toolchain to use
for all of this code.`,
			}
		}
		settings.toolchainLabel = l.defaultToolchain
	}

	return nil
}

// loadBuildConfig loads the buildfile into the provided toolchain settings object.
func (l *Loader) loadFile(file fs.SourceFile, settings *Settings) ([]graph.Item, error) {
	root, err := l.inputFileManager.LoadFile(syntax.LocationRange{}, l.buildSettings, file)
	if err != nil {
		return nil, err
	}

	var items []graph.Item
	scope := settings.NewScope()
	ctx := scope.ExecContext().(*scopeContext)
	ctx.sourceDir = file.Dir()
	ctx.itemCollector = func(item graph.Item) {
		items = append(items, item)
	}

	_, err = resolve.ExecuteNode(root, scope)
	if err != nil {
		return nil, err
	}

	return items, nil
}
