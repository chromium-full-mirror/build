// Copyright 2025 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninja

import (
	"context"
	"errors"
	"fmt"
)

type buildError struct {
	err error
}

func (b buildError) Error() string {
	return b.err.Error()
}

type flagError struct {
	err error
}

func (f flagError) Error() string {
	return f.err.Error()
}

var errNothingToDo = errors.New("nothing to do")

type errAlreadyLocked struct {
	err     error
	bufErr  error
	fname   string
	pidfile string
	owner   string
}

func (l errAlreadyLocked) Error() string {
	if l.bufErr != nil && l.pidfile != "" {
		return fmt.Sprintf("%s is locked, and failed to read %s: %v", l.fname, l.pidfile, l.bufErr)
	} else if l.bufErr != nil {
		return fmt.Sprintf("%s is locked, and failed to read: %v", l.fname, l.bufErr)
	}
	return fmt.Sprintf("%s is locked by %s: %v", l.fname, l.owner, l.err)
}
func (l errAlreadyLocked) Unwrap() error {
	if l.err != nil {
		return l.err
	}
	return l.bufErr
}

type errInterrupted struct{}

func (errInterrupted) Error() string        { return "interrupt by signal" }
func (errInterrupted) Is(target error) bool { return target == context.Canceled }
