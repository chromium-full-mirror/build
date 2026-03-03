// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjabuild

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/golang/glog"
	"google.golang.org/protobuf/proto"

	"go.chromium.org/build/siso/build"
	pb "go.chromium.org/build/siso/build/ninjabuild/proto"
	"go.chromium.org/build/siso/hashfs"
	"go.chromium.org/build/siso/o11y/clog"
	"go.chromium.org/build/siso/reapi/digest"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

// DepsLogState is a state of a deps log entry.
type DepsLogState int

const (
	DepsLogUnknown DepsLogState = iota
	DepsLogStale
	DepsLogValid
)

func (s DepsLogState) String() string {
	switch s {
	case DepsLogUnknown:
		return "UNKNOWN"
	case DepsLogStale:
		return "STALE"
	case DepsLogValid:
		return "VALID"
	}
	return fmt.Sprintf("DepsLogState[%d]", int(s))
}

func (s DepsLogState) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

// CheckDepsLogState checks deps log state by its output file.
// TODO(b/374196367): use digest for validity of output.
func CheckDepsLogState(ctx context.Context, hashFS *hashfs.HashFS, bpath *build.Path, target string, depsTime time.Time) (DepsLogState, string) {
	fi, err := hashFS.Stat(ctx, bpath.ExecRoot, bpath.MaybeFromWD(ctx, target))
	if err != nil {
		return DepsLogStale, fmt.Sprintf("not found deps output %q: %v", target, err)
	}
	if fi.ModTime().After(depsTime) {
		return DepsLogStale, fmt.Sprintf("output mtime %q newer than log: fs=%v depslog=%v", target, fi.ModTime(), depsTime)
	} else if fi.ModTime().Before(depsTime) {
		return DepsLogStale, fmt.Sprintf("output mtime %q older than log: fs=%v depslog=%v", target, fi.ModTime(), depsTime)
	}
	return DepsLogValid, ""
}

// DepsLog is an in-memory representation of siso's depslog.
// It supports creating new depslog files and reading existing depslog files,
// as well as adding new records to open depslog files.
type DepsLog struct {
	fname string

	// either is active, non-nil. other is nil.
	legacy  *ninjautil.DepsLog
	depsLog *depsLog
}

// NewDepsLog reads or creates a new deps log.
// If there are read errors, returns a truncated deps log.
func NewDepsLog(ctx context.Context, fname string) (*DepsLog, error) {
	started := time.Now()
	d, err := newDepsLog(ctx, fname)
	if errors.Is(err, errBrokenDepsLog) {
		createNewDepsLogFile(ctx, fname)
		d, err = newDepsLog(ctx, fname)
	}
	if err == nil {
		err = d.recompactIfNeeded(ctx)
		if err != nil {
			clog.Warningf(ctx, "failed to recompact deps log: %v", err)
			return nil, err
		}
		clog.Infof(ctx, "use new deps_log %s in %s", fname, time.Since(started))
		return &DepsLog{
			fname:   fname,
			depsLog: d,
		}, nil
	}
	clog.Infof(ctx, "fallback to legacy deps_log: %v", err)
	// TODO: use new deps log if it doesn't exist.

	// fallback to ninja compat deps log.
	legacy, err := ninjautil.NewDepsLog(ctx, fname)
	if err != nil {
		return nil, err
	}
	if legacy.NeedsRecompact() {
		err = legacy.Recompact(ctx)
		if err != nil {
			clog.Warningf(ctx, "failed to recompact deps log: %v", err)
			return nil, err
		}
	}
	clog.Infof(ctx, "use legacy deps_log %s in %s", fname, time.Since(started))
	return &DepsLog{
		fname:  fname,
		legacy: legacy,
	}, nil
}

// Reset resets deps log, so recorded entries is available for RetrievePaths/IDs.
func (d *DepsLog) Reset() {
	switch {
	case d.depsLog != nil:
		d.depsLog.Reset()
	case d.legacy != nil:
		d.legacy.Reset()
	}
}

// Close closes the deps log.
func (d *DepsLog) Close() error {
	switch {
	case d.depsLog != nil:
		return d.depsLog.Close()
	case d.legacy != nil:
		return d.legacy.Close()
	}
	return nil
}

// RetrievePaths returns deps log for the output, converting from id to path.
// TODO(b/374196367): return digest of output.
func (d *DepsLog) RetrievePaths(ctx context.Context, output string) ([]string, time.Time, error) {
	switch {
	case d.depsLog != nil:
		deps, mtime, _, err := d.depsLog.RetrievePaths(ctx, output)
		return deps, mtime, err
	case d.legacy != nil:
		return d.legacy.RetrievePaths(ctx, output)
	}
	return nil, time.Time{}, errors.New("no deps log")
}

// RetrieveIDs returns deps log for the output.
// TODO(b/374196367): return digest of output.
func (d *DepsLog) RetrieveIDs(ctx context.Context, out string) ([]int, time.Time, error) {
	switch {
	case d.depsLog != nil:
		deps, mtime, _, err := d.depsLog.RetrieveIDs(ctx, out)
		return deps, mtime, err
	case d.legacy != nil:
		return d.legacy.RetrieveIDs(ctx, out)
	}
	return nil, time.Time{}, errors.New("no deps log")
}

// NumPaths returns number of paths read at startup time.
func (d *DepsLog) NumPaths() int {
	switch {
	case d.depsLog != nil:
		return d.depsLog.NumPaths()
	case d.legacy != nil:
		return d.legacy.NumPaths()
	}
	return 0
}

// Path returns pathname for path id.
func (d *DepsLog) Path(id int) (string, error) {
	switch {
	case d.depsLog != nil:
		return d.depsLog.Path(id)
	case d.legacy != nil:
		return d.legacy.Path(id)
	}
	return "", errors.New("no deps log")
}

// Record records deps log for the output. This will write to disk.
// Returns whether any deps were updated.
// TODO(b/374196367): record digest of output.
func (d *DepsLog) Record(ctx context.Context, output string, mtime time.Time, deps []string) (bool, error) {
	switch {
	case d.depsLog != nil:
		return d.depsLog.Record(ctx, output, mtime, digest.Digest{}, deps)
	case d.legacy != nil:
		return d.legacy.Record(ctx, output, mtime, deps)
	}
	return false, nil
}

// RecordedTargets returns a list of targets that have deps log.
func (d *DepsLog) RecordedTargets() []string {
	switch {
	case d.depsLog != nil:
		return d.depsLog.RecordedTargets()
	case d.legacy != nil:
		return d.legacy.RecordedTargets()
	}
	return nil
}

const depsLogFileSignature = "# siso_deps\n"
const depsLogCurrentVersion = 1

// File format
//
//  0000: | <signature 12bytes> | <version 4 bytes> |
//  0010: | <len 4 bytes> | <serialized proto 4 bytes align ...>|
//
//  version/len in little endian int32.
//
// https://protobuf.dev/programming-guides/techniques/#streaming

type depsLog struct {
	fname string

	// read-only data for Get.
	rPaths   []string
	rPathIdx map[string]int
	rDeps    []*depsRecord

	mu      sync.Mutex
	paths   []string
	pathIdx map[string]int
	deps    []*depsRecord

	w *os.File

	needsRecompact bool
}

type depsRecord struct {
	mtime  int64
	digest digest.Digest
	inputs []int // index of depsLog.rPaths/paths.
}

var errUnexpectedSignature = errors.New("unexpected signature")
var errBrokenDepsLog = errors.New("broken deps log")

func newDepsLog(ctx context.Context, fname string) (*depsLog, error) {
	if fname == "" {
		return nil, errors.New("no siso_deps")
	}
	d := &depsLog{
		fname:    fname,
		rPathIdx: make(map[string]int),
		pathIdx:  make(map[string]int),
	}
	err := d.readDepsLog(ctx)
	if err != nil {
		return nil, err
	}
	return d, nil
}

func (d *depsLog) readDepsLog(ctx context.Context) error {
	f, err := os.Open(d.fname)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := d.verifySignature(ctx, f); err != nil {
		return fmt.Errorf("%w: %v", errUnexpectedSignature, err)
	}
	if err := d.verifyVersion(ctx, f); err != nil {
		return fmt.Errorf("%w: %v", errBrokenDepsLog, err)
	}
	headerOffset, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("%w: %v", errBrokenDepsLog, err)
	}
	fbuf, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("%w: %v", errBrokenDepsLog, err)
	}
	rd := bytes.NewReader(fbuf)
	const maxRecordSize = 4 * 1024 * 1024
	buf := make([]byte, maxRecordSize+1)
	m := &pb.DepsLogRecord{}
	var offset int64
	totalRecords := 0
	uniqueRecords := 0
	broken := false
readLoop:
	for {
		offset, err = rd.Seek(0, io.SeekCurrent)
		if log.V(3) {
			clog.Infof(ctx, "offset=%d: %v", headerOffset+offset, err)
		}
		if err != nil {
			clog.Warningf(ctx, "failed to get offset: %v", err)
			broken = true
			break readLoop
		}
		n, err := d.readDepsRecord(ctx, rd, buf)
		if log.V(3) {
			clog.Infof(ctx, "read record at %d-> %d: %v", headerOffset+offset, n, err)
		}
		if errors.Is(err, io.EOF) {
			break readLoop
		}
		if err != nil {
			clog.Warningf(ctx, "failed to read record at %d: %v", headerOffset+offset, err)
			broken = true
			break readLoop
		}
		m.Reset()
		err = proto.Unmarshal(buf[:n], m)
		if err != nil {
			clog.Warningf(ctx, "failed to unmarshal record at %d: %v", headerOffset+offset, err)
			broken = true
			break readLoop
		}
		for _, path := range m.Paths {
			if path.Id != int64(len(d.paths)) {
				clog.Warningf(ctx, "unexpected path id %d; want %d", path.Id, len(d.paths))
				broken = true
				break readLoop
			}
			d.pathIdx[path.Pathname] = len(d.paths)
			d.paths = append(d.paths, path.Pathname)
		}
	depsRecordLoop:
		for _, deps := range m.Deps {
			if deps.OutId < 0 || deps.OutId >= int64(len(d.paths)) {
				clog.Warningf(ctx, "bad path id=%d (d.paths=%d)", deps.OutId, len(d.paths))
				broken = true
				continue
			}
			inputs := make([]int, len(deps.InputIds))
			for i, input := range deps.InputIds {
				if input < 0 || input >= int64(len(d.paths)) {
					clog.Warningf(ctx, "bad path id=%d (d.paths=%d)", input, len(d.paths))
					broken = true
					continue depsRecordLoop
				}
				inputs[i] = int(input)
			}
			rec := &depsRecord{
				mtime: deps.OutMtime,
				digest: digest.Digest{
					Hash:      deps.OutHash,
					SizeBytes: deps.OutSizeBytes,
				},
				inputs: inputs,
			}
			totalRecords++
			if !d.update(ctx, deps.OutId, rec) {
				uniqueRecords++
			}
		}
		if broken {
			break readLoop
		}
	}

	// need recompact the log if there are too many dead records.
	// https://github.com/ninja-build/ninja/blob/36843d387cb0621c1a288179af223d4f1410be73/src/deps_log.cc#L280
	const minCompactionEntryCount = 1000
	const compactionRatio = 3
	d.needsRecompact = broken || (totalRecords > minCompactionEntryCount && totalRecords > uniqueRecords*compactionRatio) || totalRecords == 0

	clog.Infof(ctx, "siso deps %s => paths=%d, deps=%d total=%d unique=%d recompact=%t (broken:%t)", d.fname, len(d.paths), len(d.deps), totalRecords, uniqueRecords, d.needsRecompact, broken)
	d.rPaths = d.paths
	maps.Copy(d.rPathIdx, d.pathIdx)
	d.rDeps = d.deps
	return nil
}

func (*depsLog) verifySignature(ctx context.Context, f io.Reader) error {
	buf := make([]byte, len(depsLogFileSignature))
	n, err := f.Read(buf)
	if log.V(3) {
		clog.Infof(ctx, "signature=%q: %d %v", buf, n, err)
	}
	if err != nil || n != len(buf) {
		return fmt.Errorf("failed to read file signature=%d: %w", n, err)
	}
	if !bytes.Equal(buf, []byte(depsLogFileSignature)) {
		return fmt.Errorf("wrong signature %q!=%q", buf, depsLogFileSignature)
	}
	return nil
}

func (*depsLog) verifyVersion(ctx context.Context, f io.Reader) error {
	var ver int32
	err := binary.Read(f, binary.LittleEndian, &ver)
	if log.V(3) {
		clog.Infof(ctx, "version=%d: %v", ver, err)
	}
	if err != nil {
		return fmt.Errorf("failed to read version: %w", err)
	}
	if ver != depsLogCurrentVersion {
		return fmt.Errorf("wrong version %d", ver)
	}
	return nil
}

func (*depsLog) readDepsRecord(ctx context.Context, f io.Reader, buf []byte) (int, error) {
	var size int32
	err := binary.Read(f, binary.LittleEndian, &size)
	if log.V(3) {
		clog.Infof(ctx, "record size=%d: %v", size, err)
	}
	if err != nil {
		return 0, fmt.Errorf("failed to read record size: %w", err)
	}
	padding := depsRecordPadding(size)
	recSize := int(size) + padding
	if recSize > len(buf) {
		return 0, fmt.Errorf("too large record %d", size)
	}
	_, err = f.Read(buf[:recSize])
	if err != nil {
		return 0, fmt.Errorf("failed to read record %d: %w", recSize, err)
	}
	return int(size), nil
}

func (d *depsLog) Close() error {
	if d == nil || d.w == nil {
		return nil
	}
	err := d.w.Close()
	d.w = nil
	return err
}

func (d *depsLog) Reset() {
	d.mu.Lock()
	d.rPaths = d.paths
	maps.Copy(d.rPathIdx, d.pathIdx)
	d.rDeps = d.deps
	d.mu.Unlock()
}

func (d *depsLog) recompactIfNeeded(ctx context.Context) error {
	if !d.needsRecompact {
		return nil
	}
	clog.Infof(ctx, ".siso_deps recompact")
	err := d.Close()
	if err != nil {
		return fmt.Errorf("failed to close before recompact: %w", err)
	}
	tempPath := d.fname + ".recompact"
	createNewDepsLogFile(ctx, tempPath)
	nd := &depsLog{
		fname:    tempPath,
		rPathIdx: make(map[string]int),
		pathIdx:  make(map[string]int),
	}
	err = nd.openForWrite()
	if err != nil {
		return err
	}

	// write out all deps again.
	for i := range len(d.rDeps) {
		deps := d.rDeps[i]
		if deps == nil {
			continue
		}
		out := d.rPaths[i]
		mtime := time.Unix(0, deps.mtime)
		inputs := make([]string, len(deps.inputs))
		for i, in := range deps.inputs {
			inputs[i] = d.rPaths[in]
		}
		_, err = nd.Record(ctx, out, mtime, deps.digest, inputs)
		if err != nil {
			nd.Close()
			return fmt.Errorf("record in recompaction: %w", err)
		}
	}
	err = nd.Close()
	if err != nil {
		return fmt.Errorf("close recompacted deps log: %w", err)
	}
	err = os.Rename(nd.fname, d.fname)
	if err != nil {
		return fmt.Errorf("rename compacted deps log: %w", err)
	}

	d.mu.Lock()
	d.rPaths = nd.paths
	maps.Copy(d.rPathIdx, nd.pathIdx)
	d.rDeps = nd.deps

	d.paths = nd.paths
	d.pathIdx = nd.pathIdx
	d.deps = nd.deps
	d.mu.Unlock()

	d.needsRecompact = false
	return nil
}

func createNewDepsLogFile(ctx context.Context, fname string) {
	os.Remove(fname)
	f, err := os.Create(fname)
	if err != nil {
		clog.Warningf(ctx, "failed to create new deps log %s: %v", fname, err)
		return
	}
	_, err = f.Write([]byte(depsLogFileSignature))
	if err != nil {
		clog.Warningf(ctx, "failed to set file signature in %s: %v", fname, err)
	}
	err = binary.Write(f, binary.LittleEndian, int32(depsLogCurrentVersion))
	if err != nil {
		clog.Warningf(ctx, "failed to set version in %s: %v", fname, err)
	}
	err = f.Close()
	if err != nil {
		clog.Warningf(ctx, "failed to close %s: %v", fname, err)
	}
	clog.Infof(ctx, "created new deps log file: %s", fname)
}

func (d *depsLog) openForWrite() error {
	var err error
	d.w, err = os.OpenFile(d.fname, os.O_APPEND|os.O_WRONLY, 0644)
	return err
}

func (d *depsLog) update(ctx context.Context, outID int64, rec *depsRecord) bool {
	existed := int(outID) < len(d.deps)
	if !existed {
		if int(outID) < cap(d.deps) {
			d.deps = d.deps[:outID+1]
		} else {
			// manually manage resizing, append would allocate ~1.5x what is needed
			// this is problematic because we need to handle lots of filenames
			newCap := ((outID + 100) / 100) * 100
			newDeps := make([]*depsRecord, outID+1, newCap)
			copy(newDeps, d.deps)
			d.deps = newDeps
		}
	}
	if log.V(3) {
		clog.Infof(ctx, "update deps out=%d deps=%v", outID, rec)
	}
	d.deps[outID] = rec
	return existed
}

var ErrNoDepsLog = errors.New("deps not found")

// RetrievePaths returns deps log for the output, converting from id to path.
func (d *depsLog) RetrievePaths(ctx context.Context, output string) ([]string, time.Time, digest.Digest, error) {
	ids, mtime, dg, err := d.RetrieveIDs(ctx, output)
	if err != nil {
		return nil, mtime, dg, err
	}
	deps := make([]string, len(ids))
	for i, id := range ids {
		path, err := d.Path(id)
		if err != nil {
			return nil, mtime, dg, fmt.Errorf("inputs[%d]=%d: %w", i, id, err)
		}
		deps[i] = path
	}
	return deps, mtime, dg, err
}

// RetrieveIDs returns deps log for the output.
func (d *depsLog) RetrieveIDs(ctx context.Context, output string) ([]int, time.Time, digest.Digest, error) {
	var mtime time.Time
	var dg digest.Digest
	if d == nil {
		return nil, mtime, dg, errors.New("no deps log")
	}
	output = filepath.ToSlash(output)
	i, found := d.rPathIdx[output]
	if !found {
		return nil, mtime, dg, ErrNoDepsLog
	}
	if i < 0 || i >= len(d.rPaths) {
		return nil, mtime, dg, fmt.Errorf("no path entry for %s %d: %w", output, i, ErrNoDepsLog)
	}
	if d.rPaths[i] != output {
		clog.Errorf(ctx, "inconsistent paths %s -> %d -> %s", output, i, d.rPaths[i])
		return nil, mtime, dg, errors.New("inconsistent path in deps log")
	}
	if i >= len(d.rDeps) {
		return nil, mtime, dg, fmt.Errorf("no deps log entry: %w", ErrNoDepsLog)
	}
	deps := d.rDeps[i]
	if deps == nil {
		return nil, mtime, dg, fmt.Errorf("no deps log entry: %w", ErrNoDepsLog)
	}
	return deps.inputs, time.Unix(0, deps.mtime), deps.digest, nil
}

// NumPaths returns number of paths read at startup time.
func (d *depsLog) NumPaths() int {
	return len(d.rPaths)
}

// Path returns pathname for path id.
func (d *depsLog) Path(id int) (string, error) {
	if id < 0 {
		return "", fmt.Errorf("index=%d (< 0)", id)
	}
	if id < len(d.rPaths) {
		return d.rPaths[id], nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if id < len(d.paths) {
		return d.paths[id], nil
	}
	return "", fmt.Errorf("index=%d (> %d)", id, len(d.paths))
}

func (d *depsLog) lookupDepRecord(i int) (*depsRecord, error) {
	if i >= len(d.deps) {
		return nil, fmt.Errorf("index=%d (> %d)", i, len(d.deps))
	}
	deps := d.deps[i]
	if deps == nil {
		return nil, fmt.Errorf("index=%d nil entry", i)
	}
	return deps, nil
}

func depsRecordPadding(sz int32) int {
	padding := 0
	if n := sz % 4; n > 0 {
		padding = 4 - int(n)
	}
	return padding
}

// Record records deps log for the output. This will write to disk.
// Returns whether any deps were updated.
func (d *depsLog) Record(ctx context.Context, output string, mtime time.Time, dg digest.Digest, deps []string) (bool, error) {
	if d == nil {
		return false, nil
	}
	output = filepath.ToSlash(output)
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.w == nil {
		err := d.openForWrite()
		if err != nil {
			return false, err
		}
	}

	var rec *pb.DepsLogRecord
	i, added := d.uniquePathIdx(output)
	if added {
		rec = &pb.DepsLogRecord{}
		rec.Paths = append(rec.Paths, &pb.PathRecord{
			Id:       int64(i),
			Pathname: output,
		})
	}
	inputs := make([]int64, 0, len(deps))
	depIDs := make([]int, 0, len(deps))
	for i, dep := range deps {
		dep = filepath.ToSlash(dep)
		di, added := d.uniquePathIdx(dep)
		deps[i] = d.paths[di]
		if added {
			if rec == nil {
				rec = &pb.DepsLogRecord{}
			}
			rec.Paths = append(rec.Paths, &pb.PathRecord{
				Id:       int64(di),
				Pathname: dep,
			})
		}
		inputs = append(inputs, int64(di))
		depIDs = append(depIDs, di)
	}
	if rec == nil {
		dr, err := d.lookupDepRecord(i)
		if err != nil {
			rec = &pb.DepsLogRecord{}
		} else {
			// Verify the stored record.
			if len(depIDs) != len(dr.inputs) {
				rec = &pb.DepsLogRecord{}
			} else if mtime.UnixNano() != dr.mtime {
				rec = &pb.DepsLogRecord{}
			} else if dg != dr.digest {
				rec = &pb.DepsLogRecord{}
			} else {
				for i, di := range dr.inputs {
					if di != depIDs[i] {
						rec = &pb.DepsLogRecord{}
					}
				}
			}
		}
	}
	if rec == nil {
		return false, nil
	}
	rec.Deps = append(rec.Deps, &pb.DepsRecord{
		OutId:        int64(i),
		OutMtime:     mtime.UnixNano(),
		OutHash:      dg.Hash,
		OutSizeBytes: dg.SizeBytes,
		InputIds:     inputs,
	})
	d.update(ctx, int64(i), &depsRecord{
		mtime:  mtime.UnixNano(),
		digest: dg,
		inputs: depIDs,
	})
	sz := proto.Size(rec)
	padding := depsRecordPadding(int32(sz))
	buf := make([]byte, 4, 4+sz+padding)
	buf, err := proto.MarshalOptions{}.MarshalAppend(buf, rec)
	if err != nil {
		return false, err
	}
	sz = len(buf) - 4
	_, err = binary.Encode(buf, binary.LittleEndian, int32(sz))
	if err != nil {
		return false, err
	}
	if padding > 0 {
		buf = append(buf, make([]byte, padding)...)
	}
	if len(buf)%4 != 0 {
		return false, fmt.Errorf("record size is not 4 byte aligned? size=%d len(buf)=%d", sz, len(buf))
	}
	n, err := d.w.Write(buf)
	if log.V(3) {
		clog.Infof(ctx, "write record sz=%d -> %d -> %d, %v", sz, len(buf), n, err)
	}
	if err != nil {
		return false, err
	}
	if n != len(buf) {
		return false, fmt.Errorf("partial write? n=%d record=%d", n, len(buf))
	}
	return true, nil
}

func (d *depsLog) uniquePathIdx(path string) (int, bool) {
	i, found := d.pathIdx[path]
	if found {
		return i, false
	}
	d.paths = append(d.paths, path)
	i = len(d.paths) - 1
	d.pathIdx[path] = i
	return i, true
}

// RecordedTargets returns a list of targets that have deps log.
func (d *depsLog) RecordedTargets() []string {
	var targets []string
	for i, target := range d.rPaths {
		if i >= len(d.rDeps) {
			break
		}
		if d.rDeps[i] == nil {
			continue
		}
		targets = append(targets, target)
	}
	return targets
}
