// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

import (
	"cmp"
	"context"
	"fmt"
	"path/filepath"
	"runtime/trace"
	"slices"
	"sync"
	"time"

	log "github.com/golang/glog"
	"golang.org/x/sync/errgroup"

	"go.chromium.org/build/siso/mmapfile"
	"go.chromium.org/build/siso/o11y/clog"
)

// fileState stores per-file states.
type fileState struct {
	mu        sync.Mutex
	edges     []*Edge
	defaults  []posNodes
	filenames []string
	subninjas []subninja
}

// subninja is the file that the subninja statement at pos loads.
type subninja struct {
	fname string
	pos   int
}

// posNodes are the nodes of the statement at pos.
type posNodes struct {
	pos   int
	nodes []*Node
}

// addDefaults adds nodes for default target of the statement at pos.
func (fs *fileState) addDefaults(pos int, nodes []*Node) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.defaults = append(fs.defaults, posNodes{pos: pos, nodes: nodes})
}

// sortDefaults puts the default targets in the order of their statements.
// Chunks add them concurrently.
func (fs *fileState) sortDefaults() {
	slices.SortStableFunc(fs.defaults, func(a, b posNodes) int { return cmp.Compare(a.pos, b.pos) })
}

// addSubninja adds the file that the subninja statement at pos loads.
func (fs *fileState) addSubninja(fname string, pos int) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.subninjas = append(fs.subninjas, subninja{fname: fname, pos: pos})
}

// fileParser is a parser of a ninja manifest file.
type fileParser struct {
	state *State
	scope *fileScope

	sema chan struct{}

	wd     string // working directory for resolving relative paths
	fname  string
	buf    []byte
	full   chunk // for accumulated numbers from chunks
	chunks []chunk

	fileState fileState

	// allocations
	ruleArena    arena[rule]
	edgeArena    arena[Edge]
	poolArena    arena[Pool]
	bindingArena arena[binding]
}

// parseFile parses a file of fname.
func (p *fileParser) parseFile(ctx context.Context, fname string) error {
	if !filepath.IsAbs(fname) {
		fname = filepath.Join(p.wd, fname)
	}

	ctx, task := trace.NewTask(ctx, "ninja:"+fname)
	defer task.End()

	p.fname = fname
	p.fileState.filenames = append(p.fileState.filenames, fname)

	t := time.Now()
	var err error
	p.buf, err = p.readFile(ctx, fname)
	if log.V(1) {
		clog.Infof(ctx, "read %s %v: %s", fname, err, time.Since(t))
	}
	if err != nil {
		return err
	}

	t = time.Now()
	err = p.parseContent(ctx)
	if err != nil {
		return fmt.Errorf("%s: %w", fname, err)
	}
	if log.V(1) {
		clog.Infof(ctx, "parseContent %s %v: %s", p.fname, err, time.Since(t))
	}

	return nil
}

// readFile maps fname into memory. The returned slice is page-cache backed
// (not on the Go heap) and is registered on State; sub-slices into it must
// not outlive Load, which releases all mappings before returning.
func (p *fileParser) readFile(ctx context.Context, fname string) ([]byte, error) {
	defer trace.StartRegion(ctx, "ninja.read").End()
	buf, err := mmapfile.Read(fname)
	if err != nil {
		return nil, err
	}
	p.state.addMmap(buf)
	return buf, nil
}

// parseContent parses ninja file.
func (p *fileParser) parseContent(ctx context.Context) error {
	defer trace.StartRegion(ctx, "ninja.parse").End()
	t := time.Now()
	p.chunks = splitIntoChunks(ctx, p.buf)
	if log.V(1) {
		clog.Infof(ctx, "split %s %d: %s", p.fname, len(p.chunks), time.Since(t))
	}

	t = time.Now()
	err := p.parseChunks(ctx)
	if log.V(1) {
		clog.Infof(ctx, "parse chunks %s %v: %s", p.fname, err, time.Since(t))
	}
	if err != nil {
		return err
	}

	t = time.Now()
	p.alloc(ctx)
	if log.V(1) {
		clog.Infof(ctx, "alloc %s: %s", p.fname, time.Since(t))
	}

	t = time.Now()
	err = p.setup(ctx)
	if log.V(1) {
		clog.Infof(ctx, "setup %s %v: %s", p.fname, err, time.Since(t))
	}
	if err != nil {
		return err
	}

	t = time.Now()
	err = p.buildGraph(ctx)
	if log.V(1) {
		clog.Infof(ctx, "build graph %s %v: %s", p.fname, err, time.Since(t))
	}
	if err != nil {
		return err
	}

	t = time.Now()
	err = p.finalize(ctx)
	if log.V(1) {
		clog.Infof(ctx, "finalize %s %v: %s", p.fname, err, time.Since(t))
	}
	if err != nil {
		return err
	}
	return nil
}

// parseChunks parses chunks into statements and counts for allocs concurrently.
func (p *fileParser) parseChunks(ctx context.Context) error {
	defer trace.StartRegion(ctx, "ninja.parseChunks").End()
	var eg errgroup.Group
	for i := range p.chunks {
		eg.Go(func() error {
			p.sema <- struct{}{}
			defer func() { <-p.sema }()

			return p.chunks[i].parseChunk(ctx)
		})
	}
	return eg.Wait()
}

// alloc allocates arenas.
func (p *fileParser) alloc(ctx context.Context) {
	defer trace.StartRegion(ctx, "ninja.alloc").End()
	p.full.start = 0
	p.full.end = len(p.buf)
	pos := p.scope.base
	for i := range p.chunks {
		ch := &p.chunks[i]
		p.full.nvar += ch.nvar
		p.full.nrule += ch.nrule
		p.full.nrulevar += ch.nrulevar
		p.full.nbuild += ch.nbuild
		p.full.nbuildvar += ch.nbuildvar
		p.full.npool += ch.npool
		p.full.npoolvar += ch.npoolvar
		p.full.ndefault += ch.ndefault
		p.full.nsubninja += ch.nsubninja
		p.full.ncomment += ch.ncomment
		for j := range ch.statements {
			ch.statements[j].pos = pos
			pos++
		}
	}
	p.scope.end = pos

	p.ruleArena.reserve(p.full.nrule)
	p.edgeArena.reserve(p.full.nbuild)
	p.poolArena.reserve(p.full.npool)
	p.bindingArena.reserve(p.full.nrulevar)

	p.scope.rules = newRuleMap(p.full.nrule)
	p.scope.bindings = newShardBindings(p.full.nvar)

	if log.V(1) {
		clog.Infof(ctx, "alloc rule=%d edge=%d pool=%d var=%d binding=%d", p.full.nrule, p.full.nbuild, p.full.npool, p.full.nvar, p.full.nrulevar)
	}

	for i := range p.chunks {
		ch := &p.chunks[i]
		ch.state = p.state
		ch.scope = p.scope
		ch.nodemap = p.state.nodeMap.localNodeMap(ch.nbuild) // estimates # of nodes

		ch.ruleArena = p.ruleArena.chunk(ch.nrule)
		ch.edgeArena = p.edgeArena.chunk(ch.nbuild)
		ch.poolArena = p.poolArena.chunk(ch.npool)
		ch.bindingArena = p.bindingArena.chunk(ch.nrulevar)
		ch.edgePathSlab = newSlab[*Node](ch.nbuild * 4)

		if log.V(2) {
			clog.Infof(ctx, "chunk#%d edge:%d", i, ch.edgeArena.len())
		}
	}
}

// setup prepares binding scopes (var, pool, rule).
func (p *fileParser) setup(ctx context.Context) error {
	defer trace.StartRegion(ctx, "ninja.setup").End()
	var eg errgroup.Group
	for i := range p.chunks {
		ch := &p.chunks[i]
		eg.Go(func() error {
			p.sema <- struct{}{}
			defer func() { <-p.sema }()

			return ch.setupInChunk(ctx)
		})
	}
	return eg.Wait()
}

// buildGraph parses build statements and rule bindings concurrently.
func (p *fileParser) buildGraph(ctx context.Context) error {
	defer trace.StartRegion(ctx, "ninja.buildGraph").End()
	var eg errgroup.Group
	for i := range p.chunks {
		ch := &p.chunks[i]
		eg.Go(func() error {
			p.sema <- struct{}{}
			defer func() { <-p.sema }()

			return ch.buildGraphInChunk(ctx, &p.fileState)
		})
	}
	return eg.Wait()
}

// finalize finalizes a file parsing.
func (p *fileParser) finalize(ctx context.Context) error {
	defer trace.StartRegion(ctx, "ninja.finalize").End()
	p.fileState.edges = make([]*Edge, 0, p.edgeArena.len())
	for i := range p.edgeArena.used() {
		edge := p.edgeArena.at(i)
		if edge.rule != nil {
			p.fileState.edges = append(p.fileState.edges, edge)
		}
	}
	p.fileState.sortDefaults()
	p.state.mergeFileState(&p.fileState)
	p.chunks = nil
	return nil
}
