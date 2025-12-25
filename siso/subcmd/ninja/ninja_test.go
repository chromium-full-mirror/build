// Copyright 2023 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"testing"
)

func TestArgsGN(t *testing.T) {
	for _, tc := range []struct {
		name string
		args string
		key  string
		want string
	}{
		{
			name: "empty",
			key:  "use_remoteexec",
		},
		{
			name: "basic",
			args: "use_remoteexec=true",
			key:  "use_remoteexec",
			want: "true",
		},
		{
			name: "space",
			args: " use_remoteexec = true \n",
			key:  "use_remoteexec",
			want: "true",
		},
		{
			name: "multipline",
			args: `
is_debug=true
use_remoteexec=true
`,
			key:  "use_remoteexec",
			want: "true",
		},
		{
			name: "commentout",
			args: `
is_debug=true
# use_remoteexec=true
`,
			key:  "use_remoteexec",
			want: "",
		},
		{
			name: "key_prefix",
			args: `
is_debug=true
no_use_remoteexec=true
`,
			key:  "use_remoteexec",
			want: "",
		},
		{
			name: "key_suffix",
			args: `
is_debug=true
use_remoteexec_more=true
`,
			key:  "use_remoteexec",
			want: "",
		},
	} {
		got := argsGN(tc.args, tc.key)
		if got != tc.want {
			t.Errorf("%s: argsGN(%q, %q)=%q; want %q", tc.name, tc.args, tc.key, got, tc.want)
		}
	}
}
