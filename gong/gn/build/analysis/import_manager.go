// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

import (
	"slices"

	"go.chromium.org/build/gong/gn/build/fs"
	"go.chromium.org/build/gong/gn/parse"
	"go.chromium.org/build/gong/gn/resolve"
)

// ImportManager caches results of scope imports for a toolchain so the results can
// be reused rather than running the imported files multiple times.
type ImportManager struct {
	inputFileManager *fs.InputFileManager
	// TODO: add mutex when we start implementing concurrent file loads.
	// TODO: alternatively use sync.Map like fs.InputFileManager?
	imports map[fs.SourceFile]*importInfo
}

// importInfo represents a load succeeding or failing.
// (We need to record errors, not just immediately return them, because multiple concurrent
// imports may be happening for the same file. All requesters should see the same error.)
type importInfo struct {
	scope *resolve.Scope
	err   error
}

// NewImportManager creates an empty import manager that uses the given input file manager.
func NewImportManager(inputFileManager *fs.InputFileManager) *ImportManager {
	return &ImportManager{
		inputFileManager: inputFileManager,
		imports:          make(map[fs.SourceFile]*importInfo),
	}
}

// DoImport does an import of the given file into the given scope.
func (im *ImportManager) DoImport(file fs.SourceFile, nodeForErr parse.Node, destScope *resolve.Scope, settings *Settings) error {
	// Circular import check using the import chain if it exists.
	if ctx, err := contextFromScope(destScope); err == nil {
		if slices.Contains(ctx.importChain, file) {
			return &ImportLoopError{
				OriginNode: parse.OriginNode{Node: nodeForErr},
				cause:      file,
				chain:      ctx.importChain,
			}
		}
	}

	// Find the cached import, otherwise run the import.
	info, ok := im.imports[file]
	if !ok {
		scope, err := im.uncachedImport(settings, im.inputFileManager, file, nodeForErr, destScope)
		if err != nil {
			// Wrap the cause in an ImportError.
			// This will create a chain of ImportErrors if the cause is also an
			// ImportError. This allows us to print the entire chain of imports
			// when an error occurs.
			err = makeImportError(nodeForErr, file, err)
		}
		info = &importInfo{
			scope: scope,
			err:   err,
		}
		im.imports[file] = info
	}

	// Stop if the import (cached or uncached) failed.
	if info.err != nil {
		return info.err
	}

	return info.scope.NonRecursiveMergeTo(destScope, resolve.ScopeMergeOptions{
		SkipPrivateVars:     true,
		DestinationMarkUsed: true, // Don't require all imported values be used.
		SourceNode:          nodeForErr,
		SourceFriendlyName:  "import",
	})
}

// uncachedImport actually parses and executes a new scope for the import file.
func (im *ImportManager) uncachedImport(settings *Settings, inputFileManager *fs.InputFileManager, file fs.SourceFile, nodeForErr parse.Node, destScope *resolve.Scope) (*resolve.Scope, error) {
	// Load the file.
	root, err := inputFileManager.LoadFile(nodeForErr.LocationRange(), settings.buildSettings, file)
	if err != nil {
		return nil, err
	}

	// Prepare the scope to execute the file's root node into.
	scope := settings.NewScope()

	// Set the import chain for the new scope.
	if ctx, err := contextFromScope(scope); err == nil {
		if destCtx, err := contextFromScope(destScope); err == nil {
			newChainLen := len(destCtx.importChain)
			newChainCap := newChainLen + 1
			newChain := make([]fs.SourceFile, newChainLen, newChainCap)
			copy(newChain, destCtx.importChain)
			ctx.importChain = append(newChain, file)
		} else {
			ctx.importChain = []fs.SourceFile{file}
		}
	}

	// TODO: need to port SetProcessingImport?
	// TODO: need to port disallow ScopePerFileProvider providing target-related vars?
	if _, err := resolve.ExecuteNode(root, scope); err != nil {
		return nil, err
	}
	return scope, nil
}
