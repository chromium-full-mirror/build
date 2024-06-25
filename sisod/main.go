// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"slices"
	"strconv"
)

const DefaultItemsPerPage = 100

var metricsPathFlag = flag.String("metrics", "", "path to siso_metrics.json")

func main() {
	flag.Parse()

	// Read metrics.
	b, err := os.ReadFile(*metricsPathFlag)
	if err != nil {
		fmt.Printf("failed to read metrics: %s\n", err)
		os.Exit(1)
	}
	b = bytes.Replace(b, []byte("\n"), []byte(","), -1)
	b = append([]byte{'['}, b...)
	if b[len(b)-1] == ',' {
		b = b[:len(b)-1]
	}
	b = append(b, ']')
	var metrics []any
	err = json.Unmarshal(b, &metrics)
	if err != nil {
		fmt.Printf("failed to unmarshal metrics: %s\n", err)
		os.Exit(1)
	}

	actionCounts := make(map[string]int)
	for _, metric := range metrics {
		if actionVal, ok := metric.(map[string]any)["action"]; ok {
			if action, ok := actionVal.(string); ok && len(action) > 0 {
				actionCounts[action]++
			}
		}
	}

	ruleCounts := make(map[string]int)
	for _, metric := range metrics {
		if ruleVal, ok := metric.(map[string]any)["rule"]; ok {
			if rule, ok := ruleVal.(string); ok && len(rule) > 0 {
				ruleCounts[rule]++
			}
		}
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/steps/", http.StatusTemporaryRedirect)
	})

	http.HandleFunc("/steps/{id}/", func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.ParseFiles(
			"base.html",
			"_step.html",
		)
		if err != nil {
			fmt.Fprintf(w, "failed to parse templates: %s\n", err)
			return
		}

		// TODO: send 404 if missing
		step := slices.IndexFunc(metrics, func(m any) bool {
			if m, _ := m.(map[string]any); m != nil {
				if s, _ := m["step_id"].(string); s != "" {
					return s == r.PathValue("id")
				}
			}
			return false
		})

		err = tmpl.ExecuteTemplate(w, "base", metrics[step])
		if err != nil {
			fmt.Fprintf(w, "failed to execute template: %s\n", err)
		}
	})

	http.HandleFunc("/steps/", func(w http.ResponseWriter, r *http.Request) {
		tmpl, err := template.New("_steps.html").Funcs(template.FuncMap{
			"currentURLHasParam": func(key string, value string) bool {
				q := r.URL.Query()
				if values, ok := q[key]; ok {
					return slices.Contains(values, value)
				}
				return false
			},
		}).ParseFiles(
			"base.html",
			"_steps.html",
		)
		if err != nil {
			fmt.Fprintf(w, "failed to parse templates: %s\n", err)
			return
		}

		actionsWanted := r.URL.Query()["action"]
		rulesWanted := r.URL.Query()["rule"]

		filteredMetrics := metrics
		if len(actionsWanted) > 0 || len(rulesWanted) > 0 {
			// Need to clone metrics otherwise deletes will propagate to the cached metrics.
			filteredMetrics = make([]any, len(metrics))
			copy(filteredMetrics, metrics)
			filteredMetrics = slices.DeleteFunc(filteredMetrics, func(m any) bool {
				shouldDelete := false
				if metric, ok := m.(map[string]any); ok {
					// Perform filtering only if filters are set for that field.
					// Ignore failed type assertions because null fields should be filtered out.
					if len(actionsWanted) > 0 {
						action, _ := metric["action"].(string)
						if !slices.Contains(actionsWanted, action) {
							shouldDelete = true
						}
					}
					if len(rulesWanted) > 0 {
						rule, _ := metric["rule"].(string)
						if !slices.Contains(rulesWanted, rule) {
							shouldDelete = true
						}
					}
				}
				return shouldDelete
			})
		}

		itemsLen := len(filteredMetrics)
		itemsPerPage, err := strconv.Atoi(r.URL.Query().Get("items_per_page"))
		if err != nil {
			itemsPerPage = DefaultItemsPerPage
		}
		requestedPage, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			requestedPage = 0
		}
		pageCount := len(filteredMetrics) / itemsPerPage
		if len(filteredMetrics)%itemsPerPage > 0 {
			pageCount++
		}

		pageFirst := 0
		pageLast := pageCount - 1
		pageIndex := max(0, min(requestedPage, pageLast))
		pageNext := min(pageIndex+1, pageLast)
		pagePrev := max(0, pageIndex-1)
		itemsFirst := pageIndex * itemsPerPage
		itemsLast := max(0, min(itemsFirst+itemsPerPage, itemsLen)-1)
		subset := filteredMetrics[itemsFirst:itemsLast]

		data := map[string]any{
			"subset":        subset,
			"page":          requestedPage,
			"page_index":    pageIndex,
			"page_first":    pageFirst,
			"page_next":     pageNext,
			"page_prev":     pagePrev,
			"page_last":     pageLast,
			"page_count":    pageCount,
			"items_first":   itemsFirst + 1,
			"items_last":    itemsLast + 1,
			"items_len":     len(filteredMetrics),
			"action_counts": actionCounts,
			"rule_counts":   ruleCounts,
		}
		err = tmpl.ExecuteTemplate(w, "base", data)
		if err != nil {
			fmt.Fprintf(w, "failed to execute template: %s\n", err)
		}
	})

	http.HandleFunc("/style.css", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "style.css")
	})

	fmt.Println("listening on http://localhost:8080/...")
	err = http.ListenAndServe(":8080", nil)
	if errors.Is(err, http.ErrServerClosed) {
		fmt.Printf("server closed\n")
	} else if err != nil {
		fmt.Printf("error starting server: %s\n", err)
		os.Exit(1)
	}
}
