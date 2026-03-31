// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package graph

import (
	"strings"
)

// SubstitutionPattern represents a string pattern that may contain {{substitutions}}.
type SubstitutionPattern struct {
	// Pattern specifies in order the substitutions used by this pattern.
	Pattern []SubstitutionPart
}

// makeSubstitutionPattern builds a new substitution pattern.
func makeSubstitutionPattern(str string) (SubstitutionPattern, error) {
	p := SubstitutionPattern{}
	cur := 0
	for {
		// Find the next pattern.
		next := strings.Index(str[cur:], "{{")
		if next == -1 {
			// No more patterns, add the rest as a literal.
			if cur < len(str) {
				p.Pattern = append(p.Pattern, SubstitutionLiteral{str[cur:]})
			}
			break
		}

		// Add literal part before the pattern.
		next += cur
		if next > cur {
			p.Pattern = append(p.Pattern, SubstitutionLiteral{str[cur:next]})
		}

		// Find the matching substitution type.
		found := false
		for _, sub := range allSubstitutions {
			if strings.HasPrefix(str[next:], sub.String()) {
				p.Pattern = append(p.Pattern, sub)
				cur = next + len(sub.String())
				found = true
				break
			}
		}
		if !found {
			return SubstitutionPattern{}, SubstitutionFormatError{invalidPart: str[next:]}
		}
	}
	return p, nil
}
