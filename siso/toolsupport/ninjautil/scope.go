// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjautil

import (
	"bytes"
	"fmt"
	"io"
	"slices"
	"sort"
)

// fileScope is ninja rule/binding scope (in a file).
type fileScope struct {
	rules    *ruleMap
	bindings *shardBindings
	parent   *fileScope
	// base is the position of the file's first statement. A subninja file
	// starts after the last statement of its parent, so a position tells
	// which file of the chain set a value.
	base int
	// end is the position after the file's last statement.
	end int
	// parentPos is the position of the subninja statement in the parent.
	parentPos int
}

// newFileScope creates new fileScope.
func newFileScope() *fileScope {
	return &fileScope{}
}

// newSubninjaScope creates the scope of the file that the subninja
// statement at pos loads.
func (s *fileScope) newSubninjaScope(pos int) *fileScope {
	return &fileScope{
		parent:    s,
		base:      s.end,
		parentPos: pos,
	}
}

// lookupRule looks up rule by name in the scope and its parents, if any.
func (s *fileScope) lookupRule(ruleName []byte) (*rule, bool) {
	r, ok := s.rules.lookupRule(ruleName)
	if ok {
		return r, true
	}
	if s.parent != nil {
		return s.parent.lookupRule(ruleName)
	}
	if string(ruleName) == "phony" {
		return phonyRule, true
	}
	return nil, false
}

// setRule sets rule in the scope.
func (s *fileScope) setRule(rule *rule) error {
	return s.rules.setRule(rule)
}

// lookupVar looks up variable binding in the scope and its parents, if any.
func (s *fileScope) lookupVar(pos int, name []byte) (evalString, bool) {
	v, ok := s.bindings.get(pos, name)
	if ok {
		return v, ok
	}
	if s.parent != nil {
		return s.parent.lookupVar(-1, name)
	}
	return evalString{}, false
}

// lookupVarBefore looks up a variable before pos as Ninja sees it while
// it reads the statement at pos: in the file that pos belongs to, and then
// in its parents before their subninja statements.
func (s *fileScope) lookupVarBefore(pos int, name []byte) (evalString, bool) {
	for ; s != nil; s = s.parent {
		if pos < s.base {
			continue
		}
		if v, ok := s.bindings.get(pos, name); ok {
			return v, true
		}
		pos = s.parentPos
	}
	return evalString{}, false
}

// setVar sets variable "name=value".
func (s *fileScope) setVar(name []byte, value evalString) {
	s.bindings.set(name, value)
}

// Print prints scope in w.
func (s *fileScope) Print(w io.Writer) {
	if s == nil {
		return
	}
	if s.parent != nil {
		s.parent.Print(w)
	}
	keys := s.bindings.keys()
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	for _, key := range keys {
		ev, _ := s.bindings.get(-1, []byte(key))
		fmt.Fprintf(w, "%s = %s\n", key, ev.v)
	}
	fmt.Fprintln(w)
}

// pathEnv is the environment for the paths of a statement. As in Ninja,
// a path sees the variables that the file defines before the statement,
// and the paths of a build statement also see its bindings.
type pathEnv struct {
	edge  *Edge // set for a build statement
	scope *fileScope
	pos   int // position of the statement
}

// lookupVar looks up a variable of the path when pos < 0. Otherwise it
// looks up a variable inside a value that the statement at pos set, as
// Ninja evaluates a value where the file sets it.
func (e *pathEnv) lookupVar(pos int, name []byte) (evalString, bool) {
	if pos >= 0 {
		return e.scope.lookupVarBefore(pos, name)
	}
	if e.edge != nil {
		if v, ok := e.edge.env.lookupVar(-1, name); ok {
			v.pos = e.pos
			return v, true
		}
	}
	return e.scope.lookupVarBefore(e.pos, name)
}

// buildScope is a binding scope for a build.
type buildScope struct {
	buf        []byte
	statements []statement
}

// lookupVar looks up var in a build statement, if any.
func (b *buildScope) lookupVar(pos int, name []byte) (evalString, bool) {
	for _, st := range slices.Backward(b.statements) {
		if pos >= 0 && st.pos >= pos {
			continue
		}
		key := bytes.TrimSpace(b.buf[st.s:st.v])
		if !bytes.Equal(key, name) {
			continue
		}
		v := b.buf[st.v+1 : st.e]
		v = bytes.TrimSpace(v)
		return evalString{
			v:   v,
			esc: bytes.Count(v, []byte{'$'}),
		}, true
	}
	return evalString{}, false
}

// set sets build bindings.
func (b *buildScope) set(buf []byte, statements []statement) {
	b.buf = buf
	b.statements = statements
}

// Print prints bindings in w.
func (b *buildScope) Print(w io.Writer) {
	if len(b.statements) == 0 {
		return
	}
	for _, st := range b.statements {
		fmt.Fprintf(w, "%s", b.buf[st.s:st.e])
	}
}
