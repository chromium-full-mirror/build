// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package execute

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	stdpath "path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/path"
)

// TestInputGroupsDigestProperty checks, over random file trees, groups and
// own inputs, that Digest without a blob store is the same with
// InputGroups as with the group paths in Inputs (the property
// key(groups) == key(flat)). The trees have files, executables, symlinks
// to files and directories inside and outside the group dir (also to the
// group dir itself and dangling), files under the group dir that aren't in
// the group, symlinked directories in group paths, unclean own inputs, a
// working directory in the group dir and overlapping groups.
func TestInputGroupsDigestProperty(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses symlinks")
	}
	ctx := t.Context()
	seeds := int64(300)
	if v := os.Getenv("INPUT_GROUPS_PROPERTY_SEEDS"); v != "" {
		// longer runs to search for counterexamples.
		if _, err := fmt.Sscan(v, &seeds); err != nil {
			t.Fatal(err)
		}
	}
	var hits, fallbacks, compared int64
	for seed := int64(1); seed <= seeds; seed++ {
		h, f, n := inputGroupsPropertySeed(ctx, t, seed)
		hits += h
		fallbacks += f
		compared += n
	}
	t.Logf("%d digests compared: subtree used %d, fell back %d", compared, hits, fallbacks)
	// The test must exercise both paths.
	if hits < compared/10 || fallbacks < compared/10 {
		t.Errorf("subtree used %d, fell back %d of %d; want both >= 10%%", hits, fallbacks, compared)
	}
}

func inputGroupsPropertySeed(ctx context.Context, t *testing.T, seed int64) (hits, fallbacks, compared int64) {
	t.Helper()
	rnd := rand.New(rand.NewSource(seed))
	dir := t.TempDir()
	mkparent := func(f string) {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, f)), 0755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(f string, exec bool) {
		mkparent(f)
		mode := os.FileMode(0644)
		if exec {
			mode = 0755
		}
		if err := os.WriteFile(filepath.Join(dir, f), []byte(fmt.Sprintf("%s %d", f, rnd.Intn(3))), mode); err != nil {
			t.Fatal(err)
		}
	}
	symlink := func(target, f string) bool {
		mkparent(f)
		if _, err := os.Lstat(filepath.Join(dir, f)); err == nil {
			return false
		}
		if err := os.Symlink(target, filepath.Join(dir, f)); err != nil {
			t.Fatal(err)
		}
		return true
	}
	gdir := "pre/tc"
	if rnd.Intn(4) == 0 {
		gdir = "pre/tc/lib"
	}
	subdirs := []string{"", "d0", "d1", "d0/e0", "d1/e1", "d2/e2/f2"}
	var groupFiles, extras []string
	nfiles := 6 + rnd.Intn(20)
	for i := range nfiles {
		sd := subdirs[rnd.Intn(len(subdirs))]
		f := stdJoin(gdir, sd, fmt.Sprintf("f%d", i))
		write(f, rnd.Intn(4) == 0)
		if rnd.Intn(5) == 0 {
			extras = append(extras, f)
			continue
		}
		groupFiles = append(groupFiles, f)
	}
	if len(groupFiles) == 0 {
		f := stdJoin(gdir, "f")
		write(f, false)
		groupFiles = append(groupFiles, f)
	}
	write("src/a.cc", false)
	write("src/b.h", false)
	write("other/x.h", false)
	symlink("a.cc", "src/link.cc")
	// symlinks in the group dir.
	targets := []string{
		"f0", "../f1", "d1", "../d1", "e1", "..", "../..", ".",
		"../../../src/a.cc", "../../other", "missing", "d0/e0",
	}
	allFiles := append(append([]string{}, groupFiles...), extras...)
	// through returns a path through the symlink f (with target) to a file
	// under the directory it resolves to, or "".
	through := func(f, target string) string {
		resolved := stdpath.Clean(stdpath.Join(stdpath.Dir(f), target))
		var cands []string
		for _, a := range allFiles {
			if rest, ok := strings.CutPrefix(a, resolved+"/"); ok {
				cands = append(cands, rest)
			}
		}
		if len(cands) == 0 {
			return ""
		}
		return f + "/" + cands[rnd.Intn(len(cands))]
	}
	nlinks := rnd.Intn(4)
	for i := range nlinks {
		sd := subdirs[rnd.Intn(3)]
		f := stdJoin(gdir, sd, fmt.Sprintf("l%d", i))
		target := targets[rnd.Intn(len(targets))]
		if !symlink(target, f) {
			continue
		}
		groupFiles = append(groupFiles, f)
		// sometimes also paths through the link.
		for k := rnd.Intn(3); k > 0; k-- {
			if p := through(f, target); p != "" {
				groupFiles = append(groupFiles, p)
			}
		}
	}
	// own symlinks into the group dir, used with paths through them.
	var ownThrough []string
	for i, target := range []string{"../" + gdir, "../" + gdir + "/d0"} {
		f := fmt.Sprintf("src/tclink%d", i)
		symlink(target, f)
		ownThrough = append(ownThrough, f)
		for range 3 {
			if p := through(f, target); p != "" {
				ownThrough = append(ownThrough, p)
			}
		}
	}
	rnd.Shuffle(len(groupFiles), func(i, j int) { groupFiles[i], groupFiles[j] = groupFiles[j], groupFiles[i] })

	hashFS, err := hashfs.New(ctx, hashfs.Option{})
	if err != nil {
		t.Fatal(err)
	}
	defer hashFS.Close(ctx)
	if err := hashFS.WaitReady(ctx); err != nil {
		t.Fatal(err)
	}
	gc := NewInputGroupCache("")

	group := InputGroup{Key: "g", Name: "g", Paths: path.Paths(groupFiles)}
	for step := range 8 {
		var inputs []string
		pick := func(cands []string) {
			if len(cands) > 0 {
				inputs = append(inputs, cands[rnd.Intn(len(cands))])
			}
		}
		inputs = append(inputs, "src/a.cc")
		for k := rnd.Intn(4); k > 0; k-- {
			switch rnd.Intn(8) {
			case 0:
				pick(groupFiles)
			case 1:
				pick(extras)
			case 2:
				inputs = append(inputs, "src/link.cc")
			case 3:
				pick(extras)
				if n := len(inputs); n > 0 && inputs[n-1] != "src/a.cc" {
					inputs[n-1] = "src/../" + inputs[n-1]
				}
			case 4:
				inputs = append(inputs, "pre")
			case 5:
				pick(ownThrough)
				pick(ownThrough)
			default:
				inputs = append(inputs, "src/b.h", "other/x.h")
			}
		}
		groups := []InputGroup{group}
		if rnd.Intn(6) == 0 {
			// a second group: disjoint, nested or the same dir.
			switch rnd.Intn(3) {
			case 0:
				groups = append(groups, InputGroup{Key: "src", Name: "src", Paths: []path.Path{"src/b.h", "src/a.cc"}})
			case 1:
				// nested in or the same dir as the group, with files
				// that aren't in the group.
				sub := groupFiles[:1]
				if len(extras) > 0 {
					sub = append([]string{extras[rnd.Intn(len(extras))]}, sub...)
				}
				groups = append(groups, InputGroup{Key: fmt.Sprintf("sub%d", step), Name: "sub", Paths: path.Paths(sub)})
			default:
				groups = append(groups, InputGroup{Key: "other", Name: "other", Paths: []path.Path{"other/x.h"}})
			}
		}
		workDir := path.Path("out")
		switch rnd.Intn(10) {
		case 0:
			workDir = path.Path(gdir + "/d0")
		case 1:
			// a directory that isn't in the group tree.
			workDir = path.Path(gdir + "/wd")
		}
		c := &Cmd{
			ID:              fmt.Sprintf("seed%d-step%d", seed, step),
			Args:            []string{"cc"},
			WorkspaceRoot:   dir,
			WorkDir:         workDir,
			Pure:            true,
			HashFS:          hashFS,
			Inputs:          path.Paths(inputs),
			InputGroups:     groups,
			InputGroupCache: gc,
		}
		// rsp file outside the group dir, in it but not in the group,
		// or a group file.
		var rspFile path.Path
		switch rnd.Intn(8) {
		case 0:
			rspFile = "out/x.rsp"
			write(string(rspFile), false)
		case 1:
			rspFile = path.Path(gdir + "/d0/x.rsp")
			write(string(rspFile), false)
		case 2:
			rspFile = path.Path(groupFiles[rnd.Intn(len(groupFiles))])
		}
		c.RSPFile = rspFile
		fc := &Cmd{
			RSPFile:       rspFile,
			ID:            c.ID,
			Args:          c.Args,
			WorkspaceRoot: dir,
			WorkDir:       workDir,
			Pure:          true,
			HashFS:        hashFS,
			Inputs:        c.flattenInputGroups(),
		}
		want, werr := fc.Digest(ctx, nil)
		st0 := gc.Stats()
		got, gerr := c.Digest(ctx, nil)
		st1 := gc.Stats()
		if (werr != nil) != (gerr != nil) {
			//goanalyzer:ignore rawstring failure message format
			t.Fatalf("seed %d step %d: errors differ: groups %v, flat %v\ngroup %q\ninputs %q", seed, step, gerr, werr, groupFiles, inputs)
		}
		if werr != nil {
			continue
		}
		compared++
		h, f := st1.Spliced-st0.Spliced, st1.Fallbacks-st0.Fallbacks
		if h+f != 1 {
			t.Fatalf("seed %d step %d: spliced %d, fallbacks %d; want one key", seed, step, h, f)
		}
		hits += h
		fallbacks += f
		if got != want {
			//goanalyzer:ignore rawstring failure message format
			t.Fatalf("seed %d step %d: Digest with groups %v != flat %v\ngdir %s workdir %s\ngroup %q\ninputs %q\nlinks: %s", seed, step, got, want, gdir, workDir, groupFiles, inputs, listLinks(dir))
		}
	}
	return hits, fallbacks, compared
}

func stdJoin(elems ...string) string {
	return filepath.ToSlash(filepath.Join(elems...))
}

func listLinks(dir string) string {
	var s string
	filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode()&os.ModeSymlink != 0 {
			tgt, _ := os.Readlink(p)
			rel, _ := filepath.Rel(dir, p)
			s += fmt.Sprintf("%s -> %s; ", rel, tgt)
		}
		return nil
	})
	return s
}
