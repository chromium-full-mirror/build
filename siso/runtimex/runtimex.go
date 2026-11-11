// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package runtimex adjusts Go runtime defaults on startup.
//
// On Windows, Go's runtime counts only the current process's primary processor
// group when initializing GOMAXPROCS (https://go.dev/issue/6296). Until
// https://go.dev/cl/857241 lands in the Go toolchain used by Siso, importing
// this package updates GOMAXPROCS at init time to count processors across all
// groups available to the process.
package runtimex
