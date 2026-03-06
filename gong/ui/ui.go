// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package ui provides functions related to UI.
package ui

import (
	"os"
	"strings"
)

// UI is a user interface.
type UI interface {
	// Printf prints message to stdout.
	Printf(string, ...any)

	// Followings will go to stderr when redirected.

	// Errorf reports error level.
	Errorf(string, ...any)
}

// Default holds the default UI interface.
// Making changes to this variable after init is undefined behavior.
// UI implementations are currently not expected to handle being changed.
var Default UI

func init() {
	// TODO: support LogUI?
	termUI := &TermUI{
		noColor: os.Getenv("NO_COLOR") != "",
	}
	Default = termUI
}

// FormatError formats provided error for printing as GN-style error.
// TODO: implement SGR colors.
func FormatError(err error) string {
	return formatError(err, false)
}

func formatError(err error, isSubErr bool) string {
	var sb strings.Builder

	if !isSubErr {
		sb.WriteString(SGR(Bold, SGR(Red, "ERROR")))
		sb.WriteString(" ")
	}

	// File name and location.
	// nolint:errorlint // Do not want errors.As, wrapped errors printed below.
	if e, ok := err.(PresentableSourceError); ok {
		locStr := e.Location().Describe(true)
		if locStr != "" {
			if isSubErr {
				sb.WriteString("See ")
			} else {
				sb.WriteString("at ")
			}
			sb.WriteString(locStr + ": ")
		}
	}

	// Error message.
	// nolint:errorlint // Do not want errors.As, wrapped errors printed below.
	if e, ok := err.(PresentableError); ok {
		sb.WriteString(e.Message() + "\n")

		// TODO: print snippet of file.

		// Optional help text.
		if e.HelpText() != "" {
			sb.WriteString(e.HelpText() + "\n")
		}
	} else {
		sb.WriteString(err.Error() + "\n")
	}

	// Sub errors.
	// nolint:errorlint // Intentional type switch to handle wrapped errors.
	switch err := err.(type) {
	case interface{ Unwrap() error }:
		sb.WriteString(formatError(err.Unwrap(), true))
	case interface{ Unwrap() []error }:
		for _, subErr := range err.Unwrap() {
			sb.WriteString(formatError(subErr, true))
		}
	}

	return sb.String()
}
