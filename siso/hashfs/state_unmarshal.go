// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package hashfs

import (
	"runtime"
	"slices"

	"golang.org/x/sync/errgroup"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	pb "go.chromium.org/build/siso/hashfs/proto"
)

// entriesFieldTag is the field number of State.entries.
const entriesFieldTag = 1

// unmarshalState unmarshals b into a State. It gives the same result as
// proto.Unmarshal, but decodes the entries, which are most of a large state,
// on several goroutines.
func unmarshalState(b []byte) (*pb.State, error) {
	var entries [][]byte
	var rest []byte
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, protowire.ParseError(n)
		}
		m := protowire.ConsumeFieldValue(num, typ, b[n:])
		if m < 0 {
			return nil, protowire.ParseError(m)
		}
		if num == entriesFieldTag && typ == protowire.BytesType {
			v, _ := protowire.ConsumeBytes(b[n:])
			entries = append(entries, v)
		} else {
			rest = append(rest, b[:n+m]...)
		}
		b = b[n+m:]
	}

	state := &pb.State{}
	if err := proto.Unmarshal(rest, state); err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return state, nil
	}
	alloc := make([]pb.Entry, len(entries))
	state.Entries = make([]*pb.Entry, len(entries))
	var eg errgroup.Group
	nworkers := max(1, min(runtime.GOMAXPROCS(0), len(entries)))
	chunk := (len(entries) + nworkers - 1) / nworkers
	start := 0
	for c := range slices.Chunk(entries, chunk) {
		dst := alloc[start : start+len(c)]
		out := state.Entries[start : start+len(c)]
		start += len(c)
		eg.Go(func() error {
			for i, e := range c {
				if err := proto.Unmarshal(e, &dst[i]); err != nil {
					return err
				}
				out[i] = &dst[i]
			}
			return nil
		})
	}
	if err := eg.Wait(); err != nil {
		return nil, err
	}
	return state, nil
}
