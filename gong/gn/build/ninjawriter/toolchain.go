// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package ninjawriter

import (
	"fmt"
	"io"
	"slices"
	"text/template"

	"go.chromium.org/build/gong/gn/build/graph"
)

// TODO: Need to escape ninja meta characters?
// TODO: Can this be merged with most of writeToolchain below?
// TODO: Better for SubstitutionPattern to have NinjaString/WriteTo/etc method
// that concatenates pattern's NinjaString?
var toolNinjaTemplate = template.Must(template.New("tool").Parse(
	`rule {{.Name}}
  command = {{range .Command.Pattern}}{{.NinjaString}}{{end}}
{{- if .Description.Pattern}}
  description = {{range .Description.Pattern}}{{.NinjaString}}{{end}}
{{- end}}
{{- if .Rspfile.Pattern}}
  rspfile = {{range .Rspfile.Pattern}}{{.NinjaString}}{{end}}
{{- end}}
{{- if .RspfileContent.Pattern}}
  rspfile_content = {{range .RspfileContent.Pattern}}{{.NinjaString}}{{end}}
{{- end}}

`))

// writeToolchain is a rudimentary stub implementation of writing a ninja toolchain out.
func writeToolchain(w io.Writer, tc *graph.Toolchain, rules []string) error {
	var names []string
	for name := range tc.Tools {
		names = append(names, name)
	}
	slices.Sort(names)

	for _, name := range names {
		err := toolNinjaTemplate.Execute(w, tc.Tools[name])
		if err != nil {
			return err
		}
	}

	for _, rule := range rules {
		_, err := fmt.Fprintln(w, rule)
		if err != nil {
			return err
		}
	}
	return nil
}
