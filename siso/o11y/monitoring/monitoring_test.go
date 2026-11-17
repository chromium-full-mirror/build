// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package monitoring

import (
	"testing"

	"go.opentelemetry.io/otel"
)

func TestSetupAlreadySetUp(t *testing.T) {
	oldHandler := otel.GetErrorHandler()
	setupMu.Lock()
	oldSetup := setup
	setup = true
	setupMu.Unlock()
	t.Cleanup(func() {
		otel.SetErrorHandler(oldHandler)
		setupMu.Lock()
		setup = oldSetup
		setupMu.Unlock()
	})

	_, err := Setup(t.Context(), "version", nil)
	if err == nil {
		t.Fatal("Setup() succeeded, want already set up error")
	}
	if got, want := err.Error(), "monitoring already set up"; got != want {
		t.Errorf("Setup() error = %q, want %q", got, want)
	}
}
