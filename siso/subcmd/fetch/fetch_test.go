// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fetch

import (
	"testing"

	"go.chromium.org/build/siso/reapi"
)

func TestParseBytestreamURI(t *testing.T) {
	const hash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	const sha1Hash = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	const sha512Hash = "cf83e1357eefb8bdf1542850d66d8007d620e4050b5715dc83f4a921d36ce9ce47d0d13c5d85f2b0ff8318d2877eec2f63b931bd47417a81a538327af927da3e"
	for _, tc := range []struct {
		name          string
		uri           string
		preFunction   string // c.reopt.DigestFunction before parsing (flag value); "" = default.
		wantDigestStr string
		wantInstance  string
		wantFunction  string // c.reopt.DigestFunction after parsing; "" = untouched.
		wantErr       bool
	}{
		{
			name:          "no digest-function segment",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/" + hash + "/123",
			wantDigestStr: hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
		},
		{
			name:          "explicit blake3 segment, as emitted by Client.FileURI",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/blake3/" + hash + "/123",
			wantDigestStr: hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
			wantFunction:  "blake3",
		},
		{
			name:          "omitted segment with 40-hex hash infers sha1",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/" + sha1Hash + "/123",
			wantDigestStr: sha1Hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
			wantFunction:  "sha1",
		},
		{
			name:          "omitted segment with 128-hex hash infers sha512",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/" + sha512Hash + "/123",
			wantDigestStr: sha512Hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
			wantFunction:  "sha512",
		},
		{
			name:          "omitted segment with 64-hex hash keeps configured blake3",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/" + hash + "/123",
			preFunction:   "blake3",
			wantDigestStr: hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
			wantFunction:  "blake3",
		},
		{
			name:          "omitted segment with 40-hex hash overrides mismatching sha256 flag",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/" + sha1Hash + "/123",
			preFunction:   "sha256",
			wantDigestStr: sha1Hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
			wantFunction:  "sha1",
		},
		{
			name:          "explicit segment wins over configured flag",
			uri:           "bytestream://remotebuildexecution.googleapis.com/projects/p/instances/default_instance/blobs/blake3/" + hash + "/123",
			preFunction:   "sha256",
			wantDigestStr: hash + "/123",
			wantInstance:  "projects/p/instances/default_instance",
			wantFunction:  "blake3",
		},
		{
			name:    "too few path elements",
			uri:     "bytestream://host/projects/p/instances/default_instance/blobs/" + hash,
			wantErr: true,
		},
		{
			name:    "extra segment that is not a digest function",
			uri:     "bytestream://host/projects/p/instances/default_instance/blobs/bogus/" + hash + "/123",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Command{reopt: new(reapi.Option)}
			c.reopt.DigestFunction = tc.preFunction
			got, err := c.parseBytestreamURI(tc.uri)
			if gotErr := err != nil; gotErr != tc.wantErr {
				t.Fatalf("parseBytestreamURI(%q) error = %v, wantErr %v", tc.uri, err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if got != tc.wantDigestStr {
				t.Errorf("digest string = %q, want %q", got, tc.wantDigestStr)
			}
			if c.reopt.Instance != tc.wantInstance {
				t.Errorf("instance = %q, want %q", c.reopt.Instance, tc.wantInstance)
			}
			if c.reopt.DigestFunction != tc.wantFunction {
				t.Errorf("digest function = %q, want %q", c.reopt.DigestFunction, tc.wantFunction)
			}
			if want := "p"; c.projectID != want {
				t.Errorf("projectID = %q, want %q", c.projectID, want)
			}
		})
	}
}
