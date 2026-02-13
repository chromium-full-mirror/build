// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import (
	"io/fs"
	"sync"

	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/syntax"
)

// InputFileManager manages loading and parsing files from disk. This doesn't
// actually have any context for executing the results, so potentially multiple
// configs could use the same input file (saving parsing).
//
// Unlike C++ GN, this implementation only performs synchronous file loads.
// However, it is still safe for concurrent use. Multiple goroutines requesting the same file
// concurrently (or in sequence) will always result in only one file load across the lifetime
// of the InputFileManager.
//
// Because this implementation expects callers to use goroutines instead of C++ GN's queue-based
// scheduler, we do not prevent mixing sync and async loads.
// (This is banned in C++ GN because it loads `import()`s synchronously, whereas deps are loaded
// asynchronously. Hence, if a async dep load was scheduled far behind in C++ GN's queue, and then
// one or more `import()` i.e. sync loads occur, at best those thread(s) must spin until the queue
// clears, and at worst may deadlock.)
type InputFileManager struct {
	// FS used for reading files, defaults to OS filesystem.
	//
	// TODO: maybe can use FS root at builddir? but this will break absolute file references.
	// e.g. "/C:/Program Files/Windows Kits/Include"
	// may need to redesign InputFileResolver etc.
	FS fs.FS
	// use sync.Map as it's expected reads significantly dominate writes
	// e.g. multiple targets with dep to same build file
	// e.g. multiple .gn importing same .gni file
	inputFiles sync.Map
}

// InputFileResolver is the interface implemented by an object that can resolve
// an input SourceFile into an absolute OS path.
type InputFileResolver interface {
	// FullPath returns the full absolute OS path corresponding to the given
	// file in the root source tree.
	FullPath(file SourceFile) string
	// FullPathSecondary returns the absolute OS path inside the secondary
	// source path. Will return an empty string if the secondary source path
	// is empty. When loading a buildfile, the FullPath should always be
	// consulted first.
	FullPathSecondary(file SourceFile) string
	// HasSecondarySourcePath returns whether an alternate directory tree to
	// find input files is available. This behavior is intended to be used when
	// BUILD.gn files can't be checked in to certain source directories for
	// whatever reason.
	HasSecondarySourcePath() bool
}

// inputFileData is InputFileManager's internal representation of file load state.
type inputFileData struct {
	once       sync.Once
	file       InputFile
	tokens     []syntax.Token
	parsedRoot parse.Node
	parseError error
}

// LoadFile loads and parses the given file, returning the root block corresponding to the parsed result.
func (m *InputFileManager) LoadFile(origin syntax.LocationRange, inputFileResolver InputFileResolver, fileName SourceFile) (parse.Node, error) {
	var data *inputFileData
	v, _ := m.inputFiles.LoadOrStore(fileName, &inputFileData{
		file: InputFile{
			name: fileName,
		},
	})
	data = v.(*inputFileData)

	data.once.Do(func() {
		root, tokens, err := doLoadFile(origin, m.FS, inputFileResolver, fileName, &data.file)
		if err != nil {
			data.parseError = err
			return
		}
		data.tokens = tokens
		data.parsedRoot = root
	})

	if data.parseError != nil {
		return nil, data.parseError
	}
	return data.parsedRoot, nil
}

// doLoadFile performs the actual load.
func doLoadFile(origin syntax.LocationRange, fs fs.FS, inputFileResolver InputFileResolver, name SourceFile, file *InputFile) (parse.Node, []syntax.Token, error) {
	// Read.
	primaryPath := inputFileResolver.FullPath(name)
	if err := file.load(fs, primaryPath); err != nil {
		wrappedErr := LoadError{
			origin: origin,
			path:   primaryPath,
			err:    err,
		}
		if inputFileResolver.HasSecondarySourcePath() {
			secondaryPath := inputFileResolver.FullPathSecondary(name)
			if err = file.load(fs, secondaryPath); err != nil {
				wrappedErr.secondaryPath = secondaryPath
				wrappedErr.secondaryErr = err
			}
		}
		return nil, nil, wrappedErr
	}

	// Tokenize.
	tokens, err := syntax.Tokenize(file)
	if err != nil {
		return nil, nil, err
	}

	// Parse.
	root, err := parse.Parse(tokens)
	if err != nil {
		return nil, nil, err
	}

	return root, tokens, nil
}
