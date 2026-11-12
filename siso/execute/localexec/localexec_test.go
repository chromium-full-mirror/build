// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package localexec

import (
	"runtime"
	"testing"
)

// initGOMAXPROCS records GOMAXPROCS at package init time, when ForkSema is
// sized. go test -cpu changes GOMAXPROCS later.
var initGOMAXPROCS = runtime.GOMAXPROCS(0)

func TestForkSemaCapacity(t *testing.T) {
	want := concurrencyLimit(initGOMAXPROCS)
	if got := ForkSema.Capacity(); got != want {
		t.Errorf("ForkSema.Capacity() = %d, want %d", got, want)
	}
}
