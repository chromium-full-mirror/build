// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execute

import (
	"context"
	"fmt"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"go.chromium.org/build/hashigo/digest"

	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/path"
	"go.chromium.org/build/siso/reapi/merkletree"
)

// InputGroup is a large set of input paths shared by many steps, e.g. the
// expansion of a toolchain phony target. Digest without a blob store uses
// the group's Merkle subtree from the InputGroupCache; the digest is the
// same as with the group's paths in Inputs.
type InputGroup struct {
	// Key identifies the group, e.g. the phony edge. Steps with the same
	// Key must have the same Paths. It must be comparable.
	Key any
	// Name is the group's name for logs, e.g. the phony target.
	Name string
	// Paths are the group's input paths, relative to WorkspaceRoot,
	// without duplicates. They are shared between steps and must not be
	// modified.
	Paths []path.Path
}

// groupSubtree is the Merkle subtree of an InputGroup. It is immutable.
type groupSubtree struct {
	// dir is the deepest directory that contains all entries of the group.
	dir path.Path
	// digest is the digest of dir with only the group's entries in it.
	digest digest.Digest
	// names are the entry names of the group, relative to the workspace
	// root.
	names map[path.Path]struct{}
	// hasSymlink is true if the group has a symlink entry.
	hasSymlink bool
}

// groupTree caches the groupSubtree of an InputGroup.
type groupTree struct {
	mu sync.Mutex
	// done is true once the group was examined with a live context.
	done bool
	// st is nil if the group can't be used as a subtree.
	st *groupSubtree
	// unusable is done && st == nil.
	unusable atomic.Bool
}

// maxGroupTreeNames bounds the names held by the group trees of an
// InputGroupCache.
const maxGroupTreeNames = 4 << 20

const (
	// verifyFirstGroupUses is the number of first spliced lookup keys
	// that are verified per group.
	verifyFirstGroupUses = 8
	// verifyGroupKeysEvery samples one in this many keys with groups.
	verifyGroupKeysEvery = 256
)

// verifyInputGroups (SISO_VERIFY_INPUT_GROUPS) verifies every lookup key
// computed with groups.
var verifyInputGroups = os.Getenv("SISO_VERIFY_INPUT_GROUPS") != ""

// InputGroupCache holds the group subtrees and the verification state of
// input groups for one build. It must be used with one HashFS and
// workspace root.
type InputGroupCache struct {
	// excludeDir is a directory (e.g. the build output dir) in which no
	// group subtree is used.
	excludeDir path.Path

	// trees maps InputGroup.Key to *groupTree.
	trees sync.Map
	// treeNames counts the names held by trees.
	treeNames atomic.Int64

	// keyUses counts, per InputGroup.Key, the spliced lookup keys.
	keyUses sync.Map
	// keys counts the lookup keys considered for verification.
	keys atomic.Int64
	// broken is set on a verification mismatch.
	broken atomic.Bool

	spliced    atomic.Int64
	partial    atomic.Int64
	fallbacks  atomic.Int64
	verified   atomic.Int64
	mismatches atomic.Int64
	logged     atomic.Int64
}

// NewInputGroupCache returns an empty InputGroupCache that uses no group
// subtree at or under excludeDir (if not empty) or above it.
func NewInputGroupCache(excludeDir path.Path) *InputGroupCache {
	return &InputGroupCache{excludeDir: excludeDir}
}

// InputGroupsSpliced reports whether the last Digest of c used the
// subtrees of any of its InputGroups.
func (c *Cmd) InputGroupsSpliced() bool {
	return len(c.splicedInputGroups) > 0
}

// SplicedInputGroups returns the InputGroups whose subtrees the last
// Digest of c used.
func (c *Cmd) SplicedInputGroups() []InputGroup {
	return c.splicedInputGroups
}

// ShouldVerifyKey reports whether the caller should verify a lookup key
// computed with groups (after Digest; spliced is
// Cmd.SplicedInputGroups) against the flat key: always with
// SISO_VERIFY_INPUT_GROUPS, for the first verifyFirstGroupUses keys that
// spliced each group, and for one in verifyGroupKeysEvery keys with
// groups.
func (gc *InputGroupCache) ShouldVerifyKey(spliced []InputGroup) bool {
	verify := verifyInputGroups
	if gc.keys.Add(1)%verifyGroupKeysEvery == 0 {
		verify = true
	}
	for _, g := range spliced {
		v, ok := gc.keyUses.Load(g.Key)
		if !ok {
			v, _ = gc.keyUses.LoadOrStore(g.Key, new(atomic.Int64))
		}
		if v.(*atomic.Int64).Add(1) <= verifyFirstGroupUses {
			verify = true
		}
	}
	if verify {
		gc.verified.Add(1)
	}
	return verify
}

// Broken reports whether a lookup key computed with groups differed from
// the flat key. Callers must not use groups then.
func (gc *InputGroupCache) Broken() bool {
	return gc.broken.Load()
}

// SkipUnusable reports whether a group in groups is known to be unusable
// as a subtree. If so, it counts the key as a fallback, and the caller
// computes the key from the flat inputs.
func (gc *InputGroupCache) SkipUnusable(groups []InputGroup) bool {
	for _, g := range groups {
		if v, ok := gc.trees.Load(g.Key); ok && v.(*groupTree).unusable.Load() {
			gc.fallbacks.Add(1)
			return true
		}
	}
	return false
}

// ReportMismatch records that the lookup key of step computed with groups
// (got) differs from the flat key (want), and turns input groups off.
func (gc *InputGroupCache) ReportMismatch(ctx context.Context, step string, groups []InputGroup, got, want string) {
	gc.mismatches.Add(1)
	gc.broken.Store(true)
	names := make([]string, 0, len(groups))
	for _, g := range groups {
		names = append(names, g.Name)
	}
	clog.Errorf(ctx, "input groups: lookup key mismatch for %s (groups %q): %s != flat %s; input groups are off for the rest of the build", step, names, got, want)
}

// InputGroupStats are the counters of an InputGroupCache.
type InputGroupStats struct {
	// Spliced and Fallbacks count the lookup keys with groups that
	// used the group subtrees, or the group paths as inputs. Partial
	// counts the spliced keys that used only some of their groups.
	Spliced, Partial, Fallbacks int64
	// Verified and Mismatches count the verified keys, and those that
	// differed from the flat key.
	Verified, Mismatches int64
}

// Stats returns the counters of gc.
func (gc *InputGroupCache) Stats() InputGroupStats {
	return InputGroupStats{
		Spliced:    gc.spliced.Load(),
		Partial:    gc.partial.Load(),
		Fallbacks:  gc.fallbacks.Load(),
		Verified:   gc.verified.Load(),
		Mismatches: gc.mismatches.Load(),
	}
}

// LogStats logs one line with the counters (as error if a verification
// found a mismatch), for example
//
//	input groups: keys=97012 spliced=96990 partial=12 fallbacks=22 verified=391 mismatches=0 off=false
//
// where keys = spliced + fallbacks.
func (gc *InputGroupCache) LogStats(ctx context.Context) {
	st := gc.Stats()
	keys := st.Spliced + st.Fallbacks
	if keys == 0 {
		return
	}
	msg := fmt.Sprintf("input groups: keys=%d spliced=%d partial=%d fallbacks=%d verified=%d mismatches=%d off=%t",
		keys, st.Spliced, st.Partial, st.Fallbacks, st.Verified, st.Mismatches, gc.Broken())
	if st.Mismatches > 0 {
		clog.Errorf(ctx, "%s", msg)
		return
	}
	clog.Infof(ctx, "%s", msg)
}

// subtree returns the subtree of g for c, or nil if g can't be used as a
// subtree.
func (gc *InputGroupCache) subtree(ctx context.Context, c *Cmd, g InputGroup) *groupSubtree {
	v, ok := gc.trees.Load(g.Key)
	if !ok {
		v, _ = gc.trees.LoadOrStore(g.Key, &groupTree{})
	}
	gt := v.(*groupTree)
	gt.mu.Lock()
	defer gt.mu.Unlock()
	if gt.done {
		return gt.st
	}
	st, err := gc.newSubtree(ctx, c, g)
	if ctx.Err() != nil {
		// entries may be missing. Don't use or cache the result.
		return nil
	}
	gt.done = true
	if err != nil {
		gt.unusable.Store(true)
		clog.Infof(ctx, "input group %s (%d paths): not used as subtree: %v", g.Name, len(g.Paths), err)
		return nil
	}
	gt.st = st
	gc.treeNames.Add(int64(len(st.names)))
	clog.Infof(ctx, "input group %s: %d paths -> subtree %s %s", g.Name, len(g.Paths), st.dir, st.digest)
	return st
}

// GroupDir returns the deepest directory that contains every path strictly
// below it. It returns an error if there is none, or if a path is a
// directory target or not clean; such paths can't be an input group.
func GroupDir(paths []path.Path) (string, error) {
	dir := ""
	for _, p := range paths {
		s := string(p)
		if strings.HasSuffix(s, "/") {
			return "", fmt.Errorf("directory target %q", p)
		}
		if stdpath.Clean(s) != s {
			return "", fmt.Errorf("not clean path %q", p)
		}
		d := stdpath.Dir(s)
		if dir == "" {
			dir = d
		} else {
			dir = CommonDir(dir, d)
		}
		if dir == "." {
			break
		}
	}
	if dir == "." || dir == "/" || dir == "" || filepath.IsAbs(dir) || !filepath.IsLocal(dir) {
		return "", fmt.Errorf("no common directory: %q", dir)
	}
	return dir, nil
}

// symlinkStaysIn reports whether the symlink target, relative to the
// symlink's directory reldir within a tree, never leaves the tree on the
// way and doesn't resolve to the tree root.
func symlinkStaysIn(reldir, target string) bool {
	if target == "" || strings.HasPrefix(target, "/") || filepath.IsAbs(target) || strings.Contains(target, `\`) {
		return false
	}
	var stack []string
	if reldir != "." && reldir != "" {
		stack = strings.Split(reldir, "/")
	}
	for elem := range strings.SplitSeq(target, "/") {
		switch elem {
		case "", ".":
		case "..":
			if len(stack) == 0 {
				return false
			}
			stack = stack[:len(stack)-1]
		default:
			stack = append(stack, elem)
		}
	}
	// A target that resolves to the tree root itself would be merged
	// into "." of the subtree.
	return len(stack) > 0
}

func (gc *InputGroupCache) newSubtree(ctx context.Context, c *Cmd, g InputGroup) (*groupSubtree, error) {
	if len(g.Paths) == 0 {
		return nil, fmt.Errorf("no paths")
	}
	dir, err := GroupDir(g.Paths)
	if err != nil {
		return nil, err
	}
	if ex := gc.excludeDir; ex != "" && (path.Path(dir).HasPrefix(ex) || ex.HasPrefix(path.Path(dir))) {
		return nil, fmt.Errorf("group dir %s overlaps %s", dir, ex)
	}
	if gc.treeNames.Load()+int64(len(g.Paths)) > maxGroupTreeNames {
		return nil, fmt.Errorf("too many cached group names")
	}
	ents, err := c.HashFS.Entries(ctx, c.WorkspaceRoot, g.Paths)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(ents) == 0 {
		return nil, fmt.Errorf("no entries")
	}
	// Set entries in name order, like the flat computation.
	sortEntriesByName(ents)
	prefix := dir + "/"
	names := make(map[path.Path]struct{}, len(ents))
	hasSymlink := false
	t := merkletree.NewPooled(c.HashFS.DigestFunction(), nil)
	defer t.Release()
	for _, e := range ents {
		name := string(e.Name)
		if stdpath.Clean(name) != name {
			return nil, fmt.Errorf("not clean entry %q", name)
		}
		if !strings.HasPrefix(name, prefix) {
			return nil, fmt.Errorf("entry %q not under %q", name, dir)
		}
		rel := strings.TrimPrefix(name, prefix)
		if e.IsSymlink() {
			if !symlinkStaysIn(stdpath.Dir(rel), e.Target) {
				return nil, fmt.Errorf("symlink %q -> %q leaves %q", name, e.Target, dir)
			}
			hasSymlink = true
		}
		names[e.Name] = struct{}{}
		e.Name = path.Path(rel)
		err := t.Set(e)
		if err != nil {
			return nil, fmt.Errorf("set %q: %w", name, err)
		}
	}
	d, err := t.Build(ctx)
	if err != nil {
		return nil, err
	}
	return &groupSubtree{dir: path.Path(dir), digest: d, names: names, hasSymlink: hasSymlink}, nil
}

// CommonDir returns the longest common directory of the slash-separated
// directories a and b, or "." if there is none.
func CommonDir(a, b string) string {
	for a != b {
		if len(a) > len(b) {
			a, b = b, a
		}
		// a is not longer than b.
		if strings.HasPrefix(b, a) && b[len(a)] == '/' {
			return a
		}
		a = stdpath.Dir(a)
		if a == "." || a == "/" {
			return "."
		}
	}
	return a
}

// inputRootDigestWithGroups computes the input root digest of c (without a
// blob store) using the subtrees of c.InputGroups in gc. spliced are the
// groups whose subtrees it used: all of them, only some of them (partial),
// or none if a group or the step's own inputs don't allow it, in which
// case the caller computes the digest from the flat inputs.
func (c *Cmd) inputRootDigestWithGroups(ctx context.Context, gc *InputGroupCache) (d digest.Digest, spliced []InputGroup, err error) {
	if len(c.TreeInputs) > 0 || c.RemoteChroot() || len(c.RemoteInputs) > 0 || c.CanonicalizeDir || c.UseSystemInput {
		return digest.Digest{}, nil, nil
	}
	gts := make([]*groupSubtree, 0, len(c.InputGroups))
	for _, g := range c.InputGroups {
		gt := gc.subtree(ctx, c, g)
		if gt == nil {
			return digest.Digest{}, nil, nil
		}
		for _, o := range gts {
			if o.dir == gt.dir || o.dir.HasPrefix(gt.dir) || gt.dir.HasPrefix(o.dir) {
				c.logGroupNotUsed(ctx, gc, "overlapping groups %s and %s", o.dir, gt.dir)
				return digest.Digest{}, nil, nil
			}
		}
		if c.WorkDir.HasPrefix(gt.dir) {
			c.logGroupNotUsed(ctx, gc, "workdir %s in group dir %s", c.WorkDir, gt.dir)
			return digest.Digest{}, nil, nil
		}
		gts = append(gts, gt)
	}
	// A group whose dir holds an own input that is not a member is
	// dropped, and its paths become own inputs. Check the inputs first,
	// and their entries (e.g. of directories and symlinks) below.
	drop := make([]bool, len(gts))
	ndrop := 0
	// inGroupDir reports whether p is at or under a group dir, and drops
	// that group if p is not a member.
	inGroupDir := func(p path.Path) bool {
		i := groupIndexAt(gts, p)
		if i < 0 {
			return false
		}
		if _, in := gts[i].names[p]; !in && !drop[i] {
			drop[i] = true
			ndrop++
			c.logGroupDropped(ctx, gc, c.InputGroups[i], "input %s in group dir %s", p, gts[i].dir)
		}
		return true
	}
	for _, p := range c.AllInputs() {
		inGroupDir(p)
	}
	if ndrop > 0 {
		return c.withoutGroups(ctx, gc, gts, drop, ndrop)
	}
	buf, err := c.inputTree(ctx)
	if err != nil {
		return digest.Digest{}, nil, fmt.Errorf("failed to get input tree for %s: %w", c, err)
	}
	defer putEntriesBuf(buf)
	ents := *buf
	// Drop own entries that are in a group.
	kept := ents[:0]
	for _, e := range ents {
		if n := string(e.Name); stdpath.Clean(n) != n {
			// may reach into a group dir via "..".
			c.logGroupNotUsed(ctx, gc, "unclean input %s", e.Name)
			return digest.Digest{}, nil, nil
		}
		if inGroupDir(e.Name) {
			continue
		}
		if e.IsSymlink() {
			// may resolve into a group dir.
			c.logGroupNotUsed(ctx, gc, "symlink input %s", e.Name)
			return digest.Digest{}, nil, nil
		}
		kept = append(kept, e)
	}
	if ndrop > 0 {
		return c.withoutGroups(ctx, gc, gts, drop, ndrop)
	}
	subtrees := make([]merkletree.TreeEntry, 0, len(gts))
	for _, gt := range gts {
		subtrees = append(subtrees, merkletree.TreeEntry{Name: gt.dir, Digest: gt.digest})
	}
	d, err = treeDigest(ctx, c.HashFS.DigestFunction(), subtrees, kept, nil)
	if err != nil {
		return digest.Digest{}, nil, err
	}
	return d, c.InputGroups, nil
}

// withoutGroups computes the digest with the groups marked in drop added
// to the own inputs, and the others spliced. spliced are the groups whose
// subtrees it used, so it is partial if not empty. It falls back (nil
// spliced) if all groups are dropped, or a dropped group has a symlink,
// since an own symlink input falls back anyway.
func (c *Cmd) withoutGroups(ctx context.Context, gc *InputGroupCache, gts []*groupSubtree, drop []bool, ndrop int) (d digest.Digest, spliced []InputGroup, err error) {
	if ndrop == len(c.InputGroups) {
		c.logGroupNotUsed(ctx, gc, "no usable group")
		return digest.Digest{}, nil, nil
	}
	var dropped, kept []InputGroup
	for i, g := range c.InputGroups {
		if !drop[i] {
			kept = append(kept, g)
			continue
		}
		if gts[i].hasSymlink {
			c.logGroupNotUsed(ctx, gc, "dropped group %s has a symlink", g.Name)
			return digest.Digest{}, nil, nil
		}
		dropped = append(dropped, g)
	}
	fc := *c
	fc.InputGroups = dropped
	fc.Inputs = fc.flattenInputGroups()
	fc.InputGroups = kept
	return fc.inputRootDigestWithGroups(ctx, gc)
}

// groupIndexAt returns the index of the group in gts whose dir p is at or
// under, or -1.
func groupIndexAt(gts []*groupSubtree, p path.Path) int {
	for i, gt := range gts {
		if p.HasPrefix(gt.dir) {
			return i
		}
	}
	return -1
}

// logGroupNotUsed logs why the groups of c are not used, for the first
// 100 log lines of gc.
func (c *Cmd) logGroupNotUsed(ctx context.Context, gc *InputGroupCache, format string, args ...any) {
	if gc.logged.Add(1) <= 100 {
		clog.Infof(ctx, "input groups not used for %s: %s", c, fmt.Sprintf(format, args...))
	}
}

// logGroupDropped logs why group g of c is not used while the others may
// be, for the first 100 log lines of gc.
func (c *Cmd) logGroupDropped(ctx context.Context, gc *InputGroupCache, g InputGroup, format string, args ...any) {
	if gc.logged.Add(1) <= 100 {
		clog.Infof(ctx, "input group %s not used for %s: %s", g.Name, c, fmt.Sprintf(format, args...))
	}
}

// flattenInputGroups returns c's inputs with the group paths appended,
// without duplicates.
func (c *Cmd) flattenInputGroups() []path.Path {
	n := len(c.Inputs)
	for _, g := range c.InputGroups {
		n += len(g.Paths)
	}
	// the paths of the last group are only looked up.
	seen := make(map[path.Path]struct{}, n-len(c.InputGroups[len(c.InputGroups)-1].Paths))
	inputs := make([]path.Path, 0, n)
	add := func(ps []path.Path, last bool) {
		for _, p := range ps {
			if _, ok := seen[p]; ok {
				continue
			}
			if !last {
				seen[p] = struct{}{}
			}
			inputs = append(inputs, p)
		}
	}
	add(c.Inputs, false)
	for i, g := range c.InputGroups {
		add(g.Paths, i == len(c.InputGroups)-1)
	}
	return inputs
}
