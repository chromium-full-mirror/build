// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

//go:build windows

package cmdutil

import (
	"strings"
	"testing"
)

func TestCheckCommandLine(t *testing.T) {
	for _, tc := range []struct {
		name    string
		args    []string
		wantErr bool
	}{
		{
			name: "empty",
			args: nil,
		},
		{
			name: "at_limit",
			// "cmd " (4) + 32762 = 32766 UTF-16 code units (+ NUL = 32767).
			args: []string{"cmd", strings.Repeat("a", 32766-4)},
		},
		{
			name: "over_limit",
			// "cmd " (4) + 32763 = 32767 UTF-16 code units (+ NUL = 32768).
			args:    []string{"cmd", strings.Repeat("a", 32767-4)},
			wantErr: true,
		},
		{
			name: "quoted_at_limit",
			// "cmd " (4) + `"` (1) + "a " (2) + 32758 + `"` (1) = 32766 (+ NUL = 32767).
			args: []string{"cmd", "a " + strings.Repeat("b", 32766-8)},
		},
		{
			name: "quoted_over_limit",
			// Raw length is 32766, but spaces cause quoting (+2) -> 32768 (+ NUL = 32769).
			args:    []string{"cmd", "a " + strings.Repeat("b", 32766-6)},
			wantErr: true,
		},
		{
			name: "non_bmp_at_limit",
			// "cmd " (4) + 32760 + "😀" (2 UTF-16 code units) = 32766 (+ NUL = 32767).
			args: []string{"cmd", strings.Repeat("a", 32764-4) + "😀"},
		},
		{
			name: "non_bmp_over_limit",
			// "cmd " (4) + 32761 + "😀" (2 UTF-16 code units) = 32767 (+ NUL = 32768).
			args:    []string{"cmd", strings.Repeat("a", 32765-4) + "😀"},
			wantErr: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCommandLine(tc.args)
			if (err != nil) != tc.wantErr {
				t.Errorf("CheckCommandLine() = %v; wantErr=%t", err, tc.wantErr)
			}
		})
	}
}
