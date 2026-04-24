// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webui

import (
	"fmt"
	"net/http"
)

func (s *WebuiServer) handleOutdirWatch(w http.ResponseWriter, r *http.Request) {
	tmpl, err := s.loadView("_watch.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	// For now, just render the template with empty data.
	// renderBuildView will add common data like outdir, revs, etc.
	err = s.renderBuildView(w, r, tmpl, map[string]any{})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}
