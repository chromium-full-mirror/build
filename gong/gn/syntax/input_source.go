// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package syntax

// InputSource abstracts input sources, which may be a file, or other virtual
// source like a string.
//
// C++ GN represents all input sources with the InputFile class, even for
// such virtual sources like strings. We abstract this so that this package
// doesn't need a dependency on filesystem concepts.
type InputSource interface {
	// DisplayName returns the name of the input source to be used for printing
	// syntax locations.
	DisplayName() string
	// Contents returns the contents of the file.
	Contents() []byte
	// Equal returns whether the input source is equivalent to the other.
	//
	// There is no expectation that this returns true if the underlying concrete
	// InputSource implementations are different, only that input sources should
	// return true if a reasonable method to check for equality passes.
	// If this is not practical, false should be returned.
	Equal(other InputSource) bool
}
