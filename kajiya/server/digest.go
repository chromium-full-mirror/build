// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package server

import (
	"fmt"
	"slices"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"go.chromium.org/build/hashigo/digest"
	repb "go.chromium.org/build/remote-apis/build/bazel/remote/execution/v2"
)

// ParseDigestFunctions parses a comma-separated list of digest-function names
// (e.g. "sha256,blake3") into the set of functions the server advertises. It
// rejects unknown and duplicate names, and ambiguous sets in which two
// functions that omit the ByteStream {digest_function} resource-name segment
// share a hash length (MD5 and MURMUR3, both 32 hex chars): an omitted
// segment could not be resolved to either.
func ParseDigestFunctions(names string) ([]digest.Function, error) {
	var fns []digest.Function
	for name := range strings.SplitSeq(names, ",") {
		fn, err := digest.ParseFunction(strings.TrimSpace(name))
		if err != nil {
			return nil, err
		}
		if slices.Contains(fns, fn) {
			return nil, fmt.Errorf("duplicate digest function %s", fn)
		}
		fns = append(fns, fn)
	}
	for i, a := range fns {
		for _, b := range fns[i+1:] {
			if a.OmitsSegment() && b.OmitsSegment() && a.HexLen() == b.HexLen() {
				return nil, fmt.Errorf("digest functions %s and %s are ambiguous (both omit the ByteStream digest-function segment and have %d-character hashes); only one of them can be enabled at a time", a, b, a.HexLen())
			}
		}
	}
	return fns, nil
}

// defaultDigestFunctions is the advertised set for an unset
// Config.DigestFunctions, allocated once so the default config's hot paths
// don't allocate per call.
var defaultDigestFunctions = []digest.Function{digest.SHA256}

// AdvertisedDigestFunctions returns the digest functions the server advertises
// and accepts. An unset Config.DigestFunctions defaults to SHA-256 only. The
// returned slice is shared and must not be modified.
func (c Config) AdvertisedDigestFunctions() []digest.Function {
	if len(c.DigestFunctions) == 0 {
		return defaultDigestFunctions
	}
	return c.DigestFunctions
}

// advertisedNames returns the advertised digest-function names for error
// messages.
func (c Config) advertisedNames() string {
	fns := c.AdvertisedDigestFunctions()
	names := make([]string, len(fns))
	for i, fn := range fns {
		names[i] = fn.String()
	}
	return strings.Join(names, ", ")
}

// lookupAdvertised resolves a non-UNKNOWN digest-function enum against the
// advertised set.
func (c Config) lookupAdvertised(v repb.DigestFunction_Value) (digest.Function, error) {
	fn, err := digest.Lookup(v)
	if err == nil && slices.Contains(c.AdvertisedDigestFunctions(), fn) {
		return fn, nil
	}
	return digest.Function{}, status.Errorf(codes.InvalidArgument, "digest function %q is not supported by this server (supported: %s)", v, c.advertisedNames())
}

// ResolveFunction resolves a request-level digest-function enum against the
// advertised set once per request. It returns the zero Function for UNKNOWN;
// the function must then be inferred per digest from the hash length (see
// ResolveWith). Services with many digests per request resolve the enum once
// with this and parse each digest with ResolveWith, instead of paying the
// enum lookup per digest via ResolveDigest.
func (c Config) ResolveFunction(v repb.DigestFunction_Value) (digest.Function, error) {
	if v == repb.DigestFunction_UNKNOWN {
		return digest.Function{}, nil
	}
	return c.lookupAdvertised(v)
}

// ResolveWith validates one digest proto against a request-level function
// resolved by ResolveFunction; the zero Function (UNKNOWN request enum) is
// inferred from the hash length as in ResolveDigest.
func (c Config) ResolveWith(fn digest.Function, d *repb.Digest) (digest.Function, digest.Digest, error) {
	if fn.IsZero() {
		var ok bool
		fn, ok = digest.InferOmittedFrom(c.AdvertisedDigestFunctions(), len(d.GetHash()))
		if !ok {
			return digest.Function{}, digest.Digest{}, status.Errorf(codes.InvalidArgument, "cannot infer digest function: no advertised digest function has %d-character hashes (supported: %s)", len(d.GetHash()), c.advertisedNames())
		}
	}
	dg, err := fn.FromProto(d)
	if err != nil {
		return digest.Function{}, digest.Digest{}, status.Errorf(codes.InvalidArgument, "invalid digest: %v", err)
	}
	return fn, dg, nil
}

// ResolveDigest resolves the digest function conveyed by a request enum for
// one digest proto and validates the digest against it. Per the REAPI rules a
// client may leave the enum UNKNOWN only for the legacy (omitted-segment)
// functions; the server then infers the function from the hash length and its
// advertised set.
func (c Config) ResolveDigest(v repb.DigestFunction_Value, d *repb.Digest) (digest.Function, digest.Digest, error) {
	fn, err := c.ResolveFunction(v)
	if err != nil {
		return digest.Function{}, digest.Digest{}, err
	}
	return c.ResolveWith(fn, d)
}

// ResolveResourceNameFunction resolves the digest function of a ByteStream
// resource name: fn is the explicit {digest_function} segment, or the zero
// Function when the segment was omitted and the function must be inferred
// from the hash length and the advertised set.
func (c Config) ResolveResourceNameFunction(fn digest.Function, hexLen int) (digest.Function, error) {
	if !fn.IsZero() {
		if !slices.Contains(c.AdvertisedDigestFunctions(), fn) {
			return digest.Function{}, status.Errorf(codes.InvalidArgument, "digest function %s is not supported by this server (supported: %s)", fn, c.advertisedNames())
		}
		return fn, nil
	}
	inferred, ok := digest.InferOmittedFrom(c.AdvertisedDigestFunctions(), hexLen)
	if !ok {
		return digest.Function{}, status.Errorf(codes.InvalidArgument, "cannot infer digest function: no advertised digest function has %d-character hashes (supported: %s)", hexLen, c.advertisedNames())
	}
	return inferred, nil
}
