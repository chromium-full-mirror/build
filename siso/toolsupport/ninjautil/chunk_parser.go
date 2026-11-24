// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"runtime/trace"
	"strconv"
	"sync"
	"time"

	log "github.com/golang/glog"

	"go.chromium.org/build/siso/o11y/clog"
)

var evalStringsPool = sync.Pool{
	New: func() any {
		slice := make([]evalString, 0, 6000)
		return &slice
	},
}

func getEvalStrings() *[]evalString {
	return evalStringsPool.Get().(*[]evalString)
}

func putEvalStrings(ptr *[]evalString) {
	// Make it available for reuse.
	*ptr = (*ptr)[:0]
	evalStringsPool.Put(ptr)
}

// chunk is a chunk in a file.
// a statement and its bindings won't across chunk boundary.
type chunk struct {
	buf        []byte // buffer for whole file.
	start, end int    // this chunk sees buf[start:end]

	statements []statement

	state *State
	scope *fileScope

	pathsBuf []evalString

	nodemap *localNodeMap

	ruleArena    arena[rule]
	edgeArena    arena[Edge]
	poolArena    arena[Pool]
	bindingArena arena[binding]
	edgePathSlab slab[*Node]

	freezeBytesArena bumpArena[byte]
	freezeStmtsArena bumpArena[statement]

	// temp env in parseBuild
	env edgeEnv

	// scratchBuf is a scratch buffer used during parsing.
	scratchBuf bytes.Buffer

	nvar              int
	nrule, nrulevar   int
	nbuild, nbuildvar int
	npool, npoolvar   int
	ndefault          int
	nsubninja         int
	ncomment          int
}

// splitIntoChunks splits buf into chunks.
func splitIntoChunks(ctx context.Context, buf []byte) []chunk {
	if len(buf) == 0 {
		return nil
	}
	defer trace.StartRegion(ctx, "ninja.split").End()
	chunkCount := runtime.GOMAXPROCS(0)
	chunkSize := max(1024*1024, len(buf)/chunkCount+1)

	// Most subninja files fit in one chunk. Avoid reserving one slot per CPU
	// for every file.
	chunks := make([]chunk, 0, (len(buf)-1)/chunkSize+1)
	start := 0
	for start < len(buf) {
		next := min(start+chunkSize, len(buf))
		if next < len(buf) {
			next = nextChunk(buf, next)
		}
		if log.V(3) {
			clog.Infof(ctx, "chunk %d..%d", start, next)
		}
		chunks = append(chunks, chunk{
			buf:              buf,
			start:            start,
			end:              next,
			freezeBytesArena: bumpArena[byte]{initialBlock: byteArenaInitialBlock, maxBlock: byteArenaMaxBlock},
			freezeStmtsArena: bumpArena[statement]{initialBlock: statementArenaInitialBlock, maxBlock: statementArenaMaxBlock},
		})
		start = next
	}
	return chunks
}

// nextChunk finds next chunk boundary in buf[i:].
func nextChunk(buf []byte, i int) int {
	for {
		n := bytes.IndexByte(buf[i:], '\n')
		if n < 0 { // EOF
			return len(buf)
		}
		i = i + n + 1 // step over \n
		if i >= 2 && buf[i-2] == '$' {
			// escaped $\n
			continue
		}
		if i >= 3 && buf[i-3] == '$' && buf[i-2] == '\r' {
			// escaped $\r\n
			continue
		}
		if i >= len(buf) { // EOF
			return len(buf)
		}
		switch ch := buf[i]; ch {
		case ' ', '\t', '#', '\r', '\n':
			// bindings, comment or empty line.
			continue
		}
		return i
	}
}

// parseChunk parses chunk into statements and counts for allocations.
func (ch *chunk) parseChunk(ctx context.Context) error {
	t := time.Now()
	buf := ch.buf
	var lastStatement statementType
	nlines := bytes.Count(buf[ch.start:ch.end], []byte{'\n'})
	ch.statements = make([]statement, 0, nlines)
loop:
	for i := ch.start; i < len(buf) && i < ch.end; {
		switch buf[i] {
		case '\n', '\r':
			// empty line
			i++
			continue
		case '#':
			// comment line
			ch.ncomment++
			j := bytes.IndexByte(buf[i+1:], '\n')
			if j < 0 { // each EOF
				break loop
			}
			i = i + j + 1
			continue

		case '\t':
			return fmt.Errorf("line:%d: tabs are not allowed, use spaces", lineno(buf, i))

		case ' ':
			e := findNextLine(buf, i)
			var st statementType
			switch lastStatement {
			case statementBuild:
				st = statementBuildVar
			case statementPool:
				st = statementPoolVar
			case statementRule:
				st = statementRuleVar
			default:
				return fmt.Errorf("line:%d: unexpected indent: %q", lineno(buf, i), buf[i:e])
			}
			v := bytes.IndexByte(buf[i:e], '=')
			if v < 0 {
				j := skipSpaces(buf[i:e], 0, whitespaceChar)
				if j == len(buf[i:e]) || buf[i+j] == '#' {
					// ignore empty line or comment line.
					i = e
					continue
				}
				return fmt.Errorf("line:%d: wrong var binding? %q", lineno(buf, i), buf[i:e])
			}
			switch st {
			case statementBuildVar:
				ch.nbuildvar++
			case statementPoolVar:
				ch.npoolvar++
			case statementRuleVar:
				ch.nrulevar++
			}
			ch.statements = append(ch.statements, statement{
				t: st,
				s: i,
				v: i + v,
				e: e,
			})
			i = e
			continue

		case 'b':
			v, ok := isStatement(buf, i, []byte("build"))
			if ok {
				e := findNextLine(buf, i)
				ch.statements = append(ch.statements, statement{
					t: statementBuild,
					s: i,
					v: v,
					e: e,
				})
				i = e
				lastStatement = statementBuild
				ch.nbuild++
				continue
			}
		case 'd':
			v, ok := isStatement(buf, i, []byte("default"))
			if ok {
				e := findNextLine(buf, i)
				ch.statements = append(ch.statements, statement{
					t: statementDefault,
					s: i,
					v: v,
					e: e,
				})
				i = e
				lastStatement = statementDefault
				ch.ndefault++
				continue
			}
		case 'i':
			if _, ok := isStatement(buf, i, []byte("include")); ok {
				return fmt.Errorf("line:%d: include is not supported; use subninja", lineno(buf, i))
			}
		case 'p':
			v, ok := isStatement(buf, i, []byte("pool"))
			if ok {
				e := findNextLine(buf, i)
				ch.statements = append(ch.statements, statement{
					t: statementPool,
					s: i,
					v: v,
					e: e,
				})
				i = e
				lastStatement = statementPool
				ch.npool++
				continue
			}
		case 'r':
			v, ok := isStatement(buf, i, []byte("rule"))
			if ok {
				e := findNextLine(buf, i)
				ch.statements = append(ch.statements, statement{
					t: statementRule,
					s: i,
					v: v,
					e: e,
				})
				i = e
				lastStatement = statementRule
				ch.nrule++
				continue
			}
		case 's':
			v, ok := isStatement(buf, i, []byte("subninja"))
			if ok {
				e := findNextLine(buf, i)
				ch.statements = append(ch.statements, statement{
					t: statementSubninja,
					s: i,
					v: v,
					e: e,
				})
				i = e
				lastStatement = statementSubninja
				ch.nsubninja++
				continue
			}
		}
		// var decl
		e := findNextLine(buf, i)
		v := bytes.IndexByte(buf[i:], '=')
		if v < 0 {
			return fmt.Errorf("line:%d: wrong var decl? %q", lineno(buf, i), buf[i:e])
		}
		ch.statements = append(ch.statements, statement{
			t: statementVarDecl,
			s: i,
			v: i + v,
			e: e,
		})
		i = e
		lastStatement = statementVarDecl
		ch.nvar++
	}

	if log.V(1) {
		clog.Infof(ctx, "scan var:%d rule:%d+%d build:%d+%d pool:%d+%d default:%d subninja:%d comment:%d: %s",
			ch.nvar, ch.nrule, ch.nrulevar,
			ch.nbuild, ch.nbuildvar,
			ch.npool, ch.npoolvar,
			ch.ndefault, ch.nsubninja, ch.ncomment,
			time.Since(t))
	}
	return nil
}

// skipStatement skips statement same as t from i.
func (ch *chunk) skipStatement(i int, t statementType) int {
	for ; i < len(ch.statements); i++ {
		ist := ch.statements[i]
		if ist.t != t {
			break
		}
	}
	return i
}

// countStatements counts number of statements that are same as t from i.
func (ch *chunk) countStatement(i int, t statementType) int {
	n := 0
	for ; i < len(ch.statements); i++ {
		ist := ch.statements[i]
		if ist.t != t {
			return n
		}
		n++
	}
	return n
}

// setupInChunk processes var declarations / pool / rule.
func (ch *chunk) setupInChunk(ctx context.Context) error {
	var err error
	for i := 0; i < len(ch.statements); {
		st := ch.statements[i]
		switch st.t {
		case statementVarDecl:
			err = ch.parseVarBinding(i, ch.scope)
			if err != nil {
				return err
			}
			i++
			continue

		case statementPool:
			i, err = ch.parsePool(i, ch.poolArena.new())
			if err != nil {
				return err
			}
			continue

		case statementRule:
			i, err = ch.parseRule(ctx, i, ch.ruleArena.new())
			if err != nil {
				return err
			}
			continue

		case statementBuild:
			// parse build later concurrently
			i = ch.skipStatement(i+1, statementBuildVar)
			continue

		case statementDefault:
			i++
			continue
		case statementSubninja:
			i++
			continue
		default:
			return fmt.Errorf("line:%d bad statement? %s %q", lineno(ch.buf, st.s), st.t, ch.buf[st.s:st.e])
		}
	}
	return nil
}

// buildGraphInChunk parses build / default / subninja,
// which requires path (evalString) evaluation.
func (ch *chunk) buildGraphInChunk(ctx context.Context, fileState *fileState) error {
	if log.V(2) {
		clog.Infof(ctx, "buildGraphInChunk statements=%d", len(ch.statements))
	}
	ch.scratchBuf.Grow(4096)
	if ch.nbuild > 0 {
		pathsPtr := getEvalStrings()
		ch.pathsBuf = *pathsPtr
		defer func() {
			*pathsPtr = ch.pathsBuf[:0]
			ch.pathsBuf = nil
			putEvalStrings(pathsPtr)
		}()
	}
	var err error
	for i := 0; i < len(ch.statements); {
		st := ch.statements[i]
		switch st.t {
		case statementVarDecl:
			i++
			continue
		case statementPool:
			i = ch.skipStatement(i+1, statementPoolVar)
			continue

		case statementRule:
			i = ch.skipStatement(i+1, statementRuleVar)
			continue

		case statementBuild:
			i, err = ch.parseBuild(i)
			if err != nil {
				return fmt.Errorf("line:%d failed to parse edge: %q: %w", lineno(ch.buf, st.s), ch.buf[st.s:st.e], err)
			}
			continue

		case statementDefault:
			// TODO: after build graph and fail if target not found?
			nodes, err := ch.parseDefault(i)
			if err != nil {
				return err
			}
			fileState.addDefaults(nodes)
			i++
			continue
		case statementSubninja:
			subninja, err := ch.parseSubninja(i)
			if err != nil {
				return err
			}
			fileState.addSubninja(subninja)
			i++
			continue
		default:
			return fmt.Errorf("line:%d bad statement? %s %q", lineno(ch.buf, st.s), st.t, ch.buf[st.s:st.e])
		}
	}
	return nil
}

// parseName parses name for variable etc in buf[s:e].
func (ch *chunk) parseName(s, e int) ([]byte, error) {
	name := ch.buf[s:e]
	name = bytes.TrimSpace(name)
	// need to validate?
	if len(name) == 0 {
		return nil, fmt.Errorf("missing name")
	}
	if bytes.ContainsAny(name, " \t:$") {
		// name may start/end with escape newlines.
		name = bytes.ReplaceAll(name, []byte("$\n"), []byte(" "))
		name = bytes.ReplaceAll(name, []byte("$\r\n"), []byte(" "))
		name = bytes.TrimSpace(name)
		// name should not include space,
		// and not expand variable for name?
		if bytes.ContainsAny(name, " \t:$") {
			return nil, fmt.Errorf("invalid name %q", name)
		}
	}
	return name, nil
}

// parseBuild parses build statement at statements[i].
func (ch *chunk) parseBuild(i int) (int, error) {
	st := ch.statements[i]
	edge := ch.edgeArena.new()
	edge.scope = ch.scope

	paths := ch.pathsBuf[:0]
	pp := newPathParser(ch.buf[st.v:st.e])
	paths, _ = pp.pathList(paths)
	implicitOuts := 0
	if pp.pipe() {
		paths, implicitOuts = pp.pathList(paths)
	}
	nouts := len(paths)
	if nouts == 0 {
		return 0, fmt.Errorf("expected output path")
	}

	if !pp.colon() {
		return 0, fmt.Errorf("expected ':'")
	}
	ruleName, err := pp.ident()
	if err != nil {
		return 0, fmt.Errorf("expect rule: %w", err)
	}
	rule, ok := ch.scope.lookupRule(ruleName)
	if !ok {
		return 0, fmt.Errorf("unknown build rule %q", ruleName)
	}
	edge.rule = rule

	paths, _ = pp.pathList(paths)
	implicit := 0
	if pp.pipe() {
		paths, implicit = pp.pathList(paths)
	}
	orderOnly := 0
	if pp.pipe2() {
		paths, orderOnly = pp.pathList(paths)
	}
	nins := len(paths) - nouts

	if pp.pipeAt() {
		paths, _ = pp.pathList(paths)
	}
	ch.pathsBuf = paths[:0]
	outs := paths[:nouts]
	ins := paths[nouts : nouts+nins]
	validations := paths[nouts+nins:]

	i++
	n := ch.countStatement(i, statementBuildVar)
	edgeBuf, edgeStmts := ch.compactBuildStatements(ch.buf, ch.statements[i:i+n])
	edge.env.set(edgeBuf, edgeStmts)
	i += n
	edge.pos = ch.statements[i-1].pos + 1
	poolName, ok := edge.rawBinding([]byte("pool"))
	if ok && len(poolName) > 0 {
		pool, ok := ch.state.lookupPool(poolName)
		if !ok {
			return 0, fmt.Errorf("unknown pool name %q", poolName)
		}
		edge.pool = pool
	} else {
		edge.pool = defaultPool
	}
	edge.nodes = ch.edgePathSlab.slice(len(outs) + len(ins) + len(validations))[:0]
	// setup ch.env for this edge to evaluate paths
	ch.env.edge = edge
	for _, out := range outs {
		n, err := ch.targetNode(&ch.env, &ch.scratchBuf, out)
		if err != nil {
			return 0, err
		}
		if !n.setInEdge(edge) {
			return 0, multipleRulesError{target: n.path}
		}
		edge.nodes = append(edge.nodes, n)
	}
	edge.nouts = uint32(len(outs))
	edge.implicitOuts = uint32(implicitOuts)
	for _, in := range ins {
		n, err := ch.targetNode(&ch.env, &ch.scratchBuf, in)
		if err != nil {
			return 0, err
		}
		edge.nodes = append(edge.nodes, n)
		if !n.hasOut.Load() {
			n.hasOut.Store(true)
		}
	}
	edge.nins = uint32(len(ins))
	edge.implicitDeps = uint32(implicit)
	edge.orderOnlyDeps = uint32(orderOnly)

	for _, validation := range validations {
		n, err := ch.targetNode(&ch.env, &ch.scratchBuf, validation)
		if err != nil {
			return 0, err
		}
		edge.nodes = append(edge.nodes, n)
	}
	ch.env.edge = nil
	return i, nil
}

// targetPath returns a normalized path for target.
func (ch *chunk) targetPath(env evalEnv, buf *bytes.Buffer, target evalString) ([]byte, error) {
	t, err := evaluate(env, buf, target)
	if err != nil {
		return nil, fmt.Errorf("evaluate %q: %w", target.v, err)
	}
	t = bytes.TrimPrefix(t, []byte("./"))
	return t, nil
}

// targetNode returns a node identified by target.
func (ch *chunk) targetNode(env evalEnv, buf *bytes.Buffer, target evalString) (*Node, error) {
	t, err := ch.targetPath(env, buf, target)
	if err != nil {
		return nil, err
	}
	return ch.nodemap.node(t), nil
}

// parseVarBinding parses a binding statements[i] and sets it in env.
func (ch *chunk) parseVarBinding(i int, env evalSetEnv) error {
	st := ch.statements[i]
	name, err := ch.parseName(st.s, st.v)
	if err != nil {
		return fmt.Errorf("line:%d invalid var name: %q: %w", lineno(ch.buf, st.s), ch.buf[st.s:st.e], err)
	}
	val, err := parseEvalString(ch.buf[st.v+1 : st.e])
	if err != nil {
		return fmt.Errorf("line:%d invalid var value: %q: %w", lineno(ch.buf, st.s), val.v, err)
	}
	val.pos = st.pos
	val.v = freezeBytes(&ch.freezeBytesArena, val.v)
	env.setVar(freezeBytes(&ch.freezeBytesArena, name), val)
	return nil
}

// compactBuildStatements copies the byte range covering stmts into the
// chunk's byte arena and rewrites the statement offsets relative to the
// new buffer.
func (ch *chunk) compactBuildStatements(buf []byte, stmts []statement) ([]byte, []statement) {
	if len(stmts) == 0 {
		return nil, nil
	}
	start := stmts[0].s
	end := stmts[len(stmts)-1].e
	out := freezeBytes(&ch.freezeBytesArena, buf[start:end])
	adj := ch.freezeStmtsArena.alloc(len(stmts))
	for i, st := range stmts {
		adj[i] = statement{
			t:   st.t,
			s:   st.s - start,
			v:   st.v - start,
			e:   st.e - start,
			pos: st.pos,
		}
	}
	return out, adj
}

// bumpArena holds a chain of []T blocks. The first block is initialBlock
// elements; each new block doubles up to maxBlock. A request larger than
// the next planned block gets an exact-size block.
type bumpArena[T any] struct {
	blocks       [][]T
	initialBlock int
	maxBlock     int
}

// alloc returns a sub-slice of n elements that the caller may fill.
func (a *bumpArena[T]) alloc(n int) []T {
	if n == 0 {
		return nil
	}
	if k := len(a.blocks); k > 0 {
		head := &a.blocks[k-1]
		if cap(*head)-len(*head) >= n {
			off := len(*head)
			*head = (*head)[:off+n]
			return (*head)[off : off+n : off+n]
		}
	}
	var next int
	if k := len(a.blocks); k > 0 {
		next = min(2*cap(a.blocks[k-1]), a.maxBlock)
	} else {
		next = a.initialBlock
	}
	size := max(n, next)
	blk := make([]T, n, size)
	a.blocks = append(a.blocks, blk)
	return a.blocks[len(a.blocks)-1][:n:n]
}

const (
	byteArenaInitialBlock      = 256
	byteArenaMaxBlock          = 64 * 1024
	statementArenaInitialBlock = 16
	statementArenaMaxBlock     = 4096
)

// freezeBytes copies src into a so the result lives independently of src.
// Free function because Go generics can't add a method only to bumpArena[byte].
func freezeBytes(a *bumpArena[byte], src []byte) []byte {
	dst := a.alloc(len(src))
	copy(dst, src)
	return dst
}

// parsePool parses pool statement at statements[i].
func (ch *chunk) parsePool(i int, pool *Pool) (int, error) {
	st := ch.statements[i]
	name, err := ch.parseName(st.v, st.e)
	if err != nil {
		return 0, fmt.Errorf("line:%d invalid pool name %q: %w", lineno(ch.buf, st.s), ch.buf[st.v:st.e], err)
	}
	poolScope := &buildScope{}
	i++
	n := ch.countStatement(i, statementPoolVar)
	poolScope.set(ch.buf, ch.statements[i:i+n])
	i += n
	v, ok := poolScope.lookupVar(ch.statements[i-1].pos+1, []byte("depth"))
	if !ok {
		return 0, fmt.Errorf("line:%d expect 'depth=' line", lineno(ch.buf, st.s))
	}
	value, err := evaluate(ch.scope, &ch.scratchBuf, v)
	if err != nil {
		return 0, fmt.Errorf("line:%d invalid pool depth %q: %w", lineno(ch.buf, st.s), v.v, err)
	}
	depth, err := strconv.Atoi(string(value))
	if err != nil || depth < 0 {
		return 0, fmt.Errorf("line:%d invalid pool depth %q: %w", lineno(ch.buf, st.s), value, err)
	}
	pool.name = string(name)
	pool.depth = depth
	ch.state.addPool(pool)
	return i, nil
}

// parseRule parses rules from statements[i:].
func (ch *chunk) parseRule(ctx context.Context, i int, rule *rule) (int, error) {
	st := ch.statements[i]
	s, err := ch.parseName(st.v, st.e)
	if err != nil {
		return 0, fmt.Errorf("line:%d invalid rule name %q: %w", lineno(ch.buf, st.s), ch.buf[st.v:st.e], err)
	}
	name := string(s)
	if log.V(3) {
		clog.Infof(ctx, "rule %q", name)
	}
	rule.name = name
	err = ch.scope.setRule(rule)
	if err != nil {
		return 0, fmt.Errorf("line:%d failed to set rule %q: %w", lineno(ch.buf, st.s), name, err)
	}
	rule.bindings = ch.bindingArena.slice(ch.countStatement(i+1, statementRuleVar))[:0]
	i, err = ch.parseRuleBindings(i+1, rule)
	if err != nil {
		return 0, err
	}
	return i, nil
}

// parseRuleBindings parses binding vars from statements[i:] and sets them in env.
func (ch *chunk) parseRuleBindings(i int, rule *rule) (int, error) {
	for ; i < len(ch.statements); i++ {
		st := ch.statements[i]
		if st.t != statementRuleVar {
			break
		}
		err := ch.parseVarBinding(i, rule)
		if err != nil {
			return i, err
		}
	}
	return i, nil
}

// parseDefault parses default statement at statements[i].
func (ch *chunk) parseDefault(i int) ([]*Node, error) {
	st := ch.statements[i]
	pp := newPathParser(ch.buf[st.v:st.e])
	paths, _ := pp.pathList(nil)
	var nodes []*Node
	for i := range paths {
		n, err := ch.targetNode(ch.scope, &ch.scratchBuf, paths[i])
		if err != nil {
			return nil, fmt.Errorf("line:%d bad default evaluate %q: %w", lineno(ch.buf, st.s), paths[i].v, err)
		}
		nodes = append(nodes, n)
	}
	return nodes, nil
}

// parseSubninja parses subninja statement at statements[i].
func (ch *chunk) parseSubninja(i int) (string, error) {
	st := ch.statements[i]
	pp := newPathParser(ch.buf[st.v:st.e])
	paths, _ := pp.pathList(nil)
	if len(paths) != 1 {
		return "", fmt.Errorf("line:%d bad subninja paths=%d", lineno(ch.buf, st.s), len(paths))
	}
	subninja, err := ch.targetPath(ch.scope, &ch.scratchBuf, paths[0])
	if err != nil {
		return "", fmt.Errorf("line:%d bad subninja %q: %w", lineno(ch.buf, st.s), paths[0].v, err)
	}
	return string(subninja), nil
}

func lineno(buf []byte, i int) int {
	n := bytes.Count(buf[:i], []byte{'\n'})
	return n + 1
}
