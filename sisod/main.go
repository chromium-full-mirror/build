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
		tmpl, err := template.ParseFiles(
			"base.html",
			"_steps.html",
		)
		if err != nil {
			fmt.Fprintf(w, "failed to parse templates: %s\n", err)
			return
		}

		itemsLen := len(metrics)
		itemsPerPage, err := strconv.Atoi(r.URL.Query().Get("items_per_page"))
		if err != nil {
			itemsPerPage = DefaultItemsPerPage
		}
		requestedPage, err := strconv.Atoi(r.URL.Query().Get("page"))
		if err != nil {
			requestedPage = 0
		}
		pageCount := len(metrics) / itemsPerPage
		if len(metrics)%itemsPerPage > 0 {
			pageCount++
		}

		pageFirst := 0
		pageLast := pageCount - 1
		pageIndex := max(0, min(requestedPage, pageLast))
		pageNext := min(pageIndex+1, pageLast)
		pagePrev := max(0, pageIndex-1)
		itemsFirst := pageIndex * itemsPerPage
		itemsLast := min(itemsFirst+itemsPerPage, itemsLen) - 1
		subset := metrics[itemsFirst:itemsLast]

		data := map[string]any{
			"subset":      subset,
			"page_index":  pageIndex,
			"page_first":  pageFirst,
			"page_next":   pageNext,
			"page_prev":   pagePrev,
			"page_last":   pageLast,
			"page_count":  pageCount,
			"items_first": itemsFirst + 1,
			"items_last":  itemsLast + 1,
			"items_len":   len(metrics),
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
