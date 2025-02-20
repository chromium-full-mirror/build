// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ui provides functions related to UI.
package ui

import (
	"errors"
	"strings"

	"go.chromium.org/build/gong/gn/syntax"
)

// FormatError formats provided GN syntax error for printing.
// TODO: implement SGR colors.
func FormatError(err syntax.Error) string {
	return formatError(err, false)
}

func formatError(err syntax.Error, isSubErr bool) string {
	var sb strings.Builder

	if !isSubErr {
		sb.WriteString("ERROR ")
	}

	// File name and location.
	locStr := err.Location().Describe(true)
	if isSubErr {
		sb.WriteString("See ")
	} else {
		sb.WriteString("at ")
	}
	sb.WriteString(locStr + ": " + err.Message() + "\n")

	// TODO: print snippet of file.

	// Optional help text.
	if err.HelpText() != "" {
		sb.WriteString(err.HelpText() + "\n")
	}

	// Sub errors.
	for _, subErr := range err.Unwrap() {
		var syntaxErr syntax.Error
		if errors.As(subErr, &syntaxErr) {
			sb.WriteString(formatError(syntaxErr, true))
		} else {
			sb.WriteString("caused by " + subErr.Error() + "\n")
		}
	}

	return sb.String()
}
