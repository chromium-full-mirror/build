// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package cmdutil

import (
	"strings"
	"testing"
)

func TestCheckCommandLineLength(t *testing.T) {
	for _, tc := range []struct {
		name    string
		cmdline string
		wantErr bool
	}{
		{
			name:    "empty",
			cmdline: "",
		},
		{
			name:    "short",
			cmdline: "python3.exe script.py",
		},
		{
			name:    "at_limit",
			cmdline: strings.Repeat("a", 32766),
		},
		{
			name:    "over_limit",
			cmdline: strings.Repeat("a", 32767),
			wantErr: true,
		},
		{
			name:    "bmp_non_ascii_at_limit",
			cmdline: strings.Repeat("a", 32765) + "あ",
		},
		{
			name:    "bmp_non_ascii_over_limit",
			cmdline: strings.Repeat("a", 32766) + "あ",
			wantErr: true,
		},
		{
			name:    "non_bmp_at_limit",
			cmdline: strings.Repeat("a", 32764) + "😀",
		},
		{
			name:    "non_bmp_over_limit",
			cmdline: strings.Repeat("a", 32765) + "😀",
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkCommandLineLength(tc.cmdline)
			if (err != nil) != tc.wantErr {
				t.Errorf("checkCommandLineLength(len=%d) = %v; wantErr=%t", len(tc.cmdline), err, tc.wantErr)
			}
		})
	}
}
