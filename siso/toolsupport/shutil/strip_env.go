// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package shutil

import "strings"

// StripEnvPrefix strips leading environment variables and shell variables (both `FOO=bar` and
// `export FOO=bar &&` formats) from the command line.
func StripEnvPrefix(cmdline string) string {
	s := cmdline
	for {
		tok, rest, ok := nextShellToken(s)
		if !ok {
			return s
		}
		if tok == "export" {
			cur := rest
			foundSep := false
			valid := true
			for {
				t, r, ok := nextShellToken(cur)
				if !ok {
					valid = false
					break
				}
				if t == "&&" || t == ";" {
					cur = r
					foundSep = true
					break
				}
				if !isShellIdent(t) && !isEnvAssignment(t) {
					valid = false
					break
				}
				cur = r
			}
			if valid && foundSep {
				s = cur
				continue
			}
			return s
		}
		if isEnvAssignment(tok) {
			s = rest
			if next, afterSep, ok := nextShellToken(s); ok && (next == "&&" || next == ";") {
				s = afterSep
			}
			continue
		}
		return s
	}
}

func nextShellToken(s string) (tok, rest string, ok bool) {
	s = strings.TrimLeft(s, " \t")
	if s == "" {
		return "", "", false
	}
	if strings.HasPrefix(s, "&&") {
		return "&&", s[2:], true
	}
	if s[0] == ';' {
		return ";", s[1:], true
	}
	var qch byte
	escaped := false
	for i := range s {
		ch := s[i]
		if escaped {
			escaped = false
			continue
		}
		if qch != 0 {
			if qch == '"' && ch == '\\' {
				escaped = true
				continue
			}
			if ch == qch {
				qch = 0
			}
			continue
		}
		switch ch {
		case ' ', '\t', '&', ';':
			return s[:i], s[i:], true
		case '`', '|', '<', '>', '(', ')', '$':
			return "", "", false
		case '\\':
			escaped = true
		case '"', '\'':
			qch = ch
		}
	}
	if qch != 0 || escaped {
		return "", "", false
	}
	return s, "", true
}

func isShellIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := range s {
		c := s[i]
		isAlpha := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_'
		if i == 0 && !isAlpha {
			return false
		}
		isDigit := c >= '0' && c <= '9'
		if !isAlpha && !isDigit {
			return false
		}
	}
	return true
}

func isEnvAssignment(tok string) bool {
	name, _, ok := strings.Cut(tok, "=")
	return ok && isShellIdent(name)
}
