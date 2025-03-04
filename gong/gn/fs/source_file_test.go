// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package fs

import "testing"

func TestMakeSourceFile(t *testing.T) {
	// The SourceFile object should normalize the input passed to the constructor.
	// The normalizer unit test checks for all the weird edge cases for normalizing
	// so here just check that it gets called.
	for _, tc := range []struct {
		path    string
		want    string
		wantErr bool
	}{
		{
			path: "//foo/../bar.cc",
			want: "//bar.cc",
		},
		{
			path: "//foo/././../bar.cc",
			want: "//bar.cc",
		},
		{
			path:    "foo/bar.cc",
			wantErr: true,
		},
		{
			path:    "//foo/bar/",
			wantErr: true,
		},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			s, err := makeSourceFile(tc.path)
			if err != nil {
				if !tc.wantErr {
					t.Errorf("makeSourceFile(%q).value.Value()=_, %v; want=%v, nil", tc.path, err, tc.want)
				}
				return
			}
			if err == nil && tc.wantErr {
				t.Errorf("makeSourceFile(%q).value.Value()=_, nil; want=%v, error", tc.path, tc.want)
				return
			}
			v := s.value.Value()
			if v != tc.want {
				t.Errorf("makeSourceFile(%q).value.Value()=%s; want=%v, nil", tc.path, v, tc.want)
			}
		})
	}
}
