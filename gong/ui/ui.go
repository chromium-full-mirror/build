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
func FormatError(err error) string {
	var sb strings.Builder
	for i, e := range collectErrors(nil, err) {
		sb.WriteString(formatError(e, i > 0))
	}
	return sb.String()
}

func collectErrors(acc []error, err error) []error {
	if err == nil {
		return acc
	}

	// Handle stack trace errors by showing the cause first.
	// nolint:errorlint // Do not use errors.As as it will check wrapped errors.
	if e, ok := err.(StackTraceError); ok {
		acc = append(acc, e.Stack()...)
		return append(acc, err)
	}

	acc = append(acc, err)

	// Sub errors.
	// nolint:errorlint // Intentional type switch to handle wrapped errors.
	switch err := err.(type) {
	case interface{ Unwrap() error }:
		return collectErrors(acc, err.Unwrap())
	case interface{ Unwrap() []error }:
		for _, subErr := range err.Unwrap() {
			acc = collectErrors(acc, subErr)
		}
	}
	return acc
}

func formatError(err error, isSubErr bool) string {
	var sb strings.Builder

	if !isSubErr {
		sb.WriteString(SGR(Bold, SGR(Red, "ERROR")))
		sb.WriteString(" ")
	}

	// File name and location.
	// nolint:errorlint // Do not use errors.As as it will check wrapped errors.
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
	// nolint:errorlint // Do not use errors.As as it will check wrapped errors.
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

	return sb.String()
}
