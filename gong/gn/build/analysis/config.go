// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package analysis

// Config is an item in the GN dependency graph that represents a named config.
//
// A config can list other configs. We track both the data assigned directly
// on the config, this list of sub-configs, and (when the config is resolved)
// the resulting values of everything merged together. The flatten step
// means we can avoid doing a recursive config walk for every target to compute
// flags.
type Config struct {
	itemInfo
}

func (Config) compatibleWith(item Item) bool {
	switch item.(type) {
	case *Config:
		return true
	}
	return false
}
