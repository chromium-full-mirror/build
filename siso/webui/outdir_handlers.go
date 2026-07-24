// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package webui

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/metadata"
	"go.chromium.org/build/siso/toolsupport/ninjautil"
)

const (
	DefaultItemsPerPage = 100

	// flatOutsub is a virtual sub-directory path segment used in URLs to route and match
	// flat (single-level) output directories (like AOSP's "out") under Go's mux routing (e.g. /out/_/builds/...)
	flatOutsub = "_"
)

type fieldAggregate struct {
	Key   string
	Count int
}

// buildMetrics represents data for a single build revision.
// (Exported fields are accessible from Go templates.)
type buildMetrics struct {
	Mtime         time.Time
	Rev           string // TODO: rename to BuildID?
	Info          *metadata.InvocationInfo
	buildDuration build.IntervalMetric
	lastStepID    string
	ruleCounts    []fieldAggregate
	actionCounts  []fieldAggregate
	// buildMetrics contains build.StepMetric related to overall build e.g. regenerate ninja files.
	buildMetrics []*build.StepMetric
	// StepMetrics contains build.StepMetric related to ninja executions.
	// TODO: rename to allow invocation.Invocation Steps() to rename to StepMetrics().
	StepMetrics []*build.StepMetric
	// stepByStepID keys step ID to *build.StepMetric for faster lookup.
	stepByStepID map[string]*build.StepMetric
	// stepByOutput keys output to *build.StepMetric for faster lookup.
	stepByOutput map[string]*build.StepMetric
}

// ID returns the build ID of this invocation.
func (b *buildMetrics) ID() string {
	return b.Rev
}

// BuildDuration returns the build duration of this invocation.
func (b *buildMetrics) BuildDuration() build.IntervalMetric {
	return b.buildDuration
}

// Steps returns the step metrics struct for this invocation.
func (b *buildMetrics) Steps() []*build.StepMetric {
	return b.StepMetrics
}

// aggregateMetric represents data for the aggregates page.
// (All fields are exported to be usable in Go templates.)
type aggregateMetric struct {
	AggregateBy           string
	Count                 int
	TotalUtime            build.IntervalMetric
	TotalDuration         build.IntervalMetric
	TotalWeightedDuration build.IntervalMetric
}

func loadBuildMetrics(metricsPath string) (*buildMetrics, error) {
	f, err := os.Open(metricsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read metrics: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to stat metrics: %w", err)
	}

	metricsData := &buildMetrics{
		Mtime:        stat.ModTime(),
		buildMetrics: []*build.StepMetric{},
		StepMetrics:  []*build.StepMetric{},
		stepByStepID: make(map[string]*build.StepMetric),
		stepByOutput: make(map[string]*build.StepMetric),
		ruleCounts:   []fieldAggregate{},
		actionCounts: []fieldAggregate{},
	}

	d := json.NewDecoder(f)
	for {
		var m build.StepMetric
		err := d.Decode(&m)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parse error in %s:%d: %w", metricsPath, d.InputOffset(), err)
		}
		if m.BuildID != "" {
			metricsData.buildMetrics = append(metricsData.buildMetrics, &m)
			// The last build metric found has the actual build duration.
			metricsData.buildDuration = m.Duration
		} else if m.StepID != "" {
			metricsData.StepMetrics = append(metricsData.StepMetrics, &m)
			metricsData.stepByStepID[m.StepID] = &m
			metricsData.stepByOutput[m.Output()] = &m
			metricsData.lastStepID = m.StepID
		} else {
			return nil, fmt.Errorf("unexpected metric found %v", m)
		}
	}

	if len(metricsData.buildMetrics) == 0 || metricsData.buildMetrics[0].BuildID == "" {
		return nil, fmt.Errorf("need at least one build_id in %s", metricsPath)
	}
	metricsData.Rev = metricsData.buildMetrics[0].BuildID

	actionCounts := make(map[string]int)
	for _, metric := range metricsData.StepMetrics {
		if metric.Action != "" {
			actionCounts[metric.Action]++
		}
	}
	for action := range maps.Keys(actionCounts) {
		metricsData.actionCounts = append(metricsData.actionCounts, fieldAggregate{
			Key:   action,
			Count: actionCounts[action],
		})
	}
	slices.SortFunc(metricsData.actionCounts, func(a, b fieldAggregate) int {
		return cmp.Compare(b.Count, a.Count)
	})

	ruleCounts := make(map[string]int)
	for _, metric := range metricsData.StepMetrics {
		if metric.Rule != "" {
			ruleCounts[metric.Rule]++
		}
	}
	for rule := range maps.Keys(ruleCounts) {
		metricsData.ruleCounts = append(metricsData.ruleCounts, fieldAggregate{
			Key:   rule,
			Count: ruleCounts[rule],
		})
	}
	slices.SortFunc(metricsData.ruleCounts, func(a, b fieldAggregate) int {
		return cmp.Compare(b.Count, a.Count)
	})

	// Attempt to load corresponding invocation metadata if available.
	dir := filepath.Dir(metricsPath)
	if matches, err := filepath.Glob(filepath.Join(dir, "siso_metadata*.json")); err == nil {
		for _, match := range matches {
			data, err := os.ReadFile(match)
			if err != nil {
				continue
			}
			var info metadata.InvocationInfo
			if err := json.Unmarshal(data, &info); err != nil {
				continue
			}
			if info.BuildID == metricsData.Rev || len(matches) == 1 {
				metricsData.Info = &info
				break
			}
		}
	}

	return metricsData, nil
}

// handleOutdirReload reloads the outdir.
func (s *WebuiServer) handleOutdirReload(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	// TODO(b/533258244): introduce concept of reloadable invocation series?
	outdirInfo, ok := series.(*outdirInfo)
	if !ok {
		s.renderBuildViewError(http.StatusNotFound, "this is not an outdir", w, r)
		return
	}

	// TODO(b/533258244): decouple from outdirInfo?
	s.outdirProvider.Invalidate(outdirInfo.pathRel)
	_, err = s.outdirProvider.Get(outdirInfo.pathRel)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to reload outdir: %v", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	// Then redirect to root page.
	http.Redirect(w, r, outdirBaseURL(r), http.StatusTemporaryRedirect)
}

// handleInvocationSeriesRoot redirects from invocation series URL to `./builds/{latestRev}/steps/`.
func (s *WebuiServer) handleInvocationSeriesRoot(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	if series.Latest() == nil {
		s.renderBuildViewError(http.StatusNotFound, "no invocations found", w, r)
		return
	}

	http.Redirect(w, r, fmt.Sprintf("%s/builds/%s/steps/", outdirBaseURL(r), series.Latest().ID()), http.StatusTemporaryRedirect)
}

func (s *WebuiServer) handleInvocationDetails(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	metrics := series.Get(r.PathValue("rev"))
	if metrics == nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("no metrics found for request %s", r.URL), w, r)
		return
	}

	tmpl, err := s.loadView("invocation_details.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"metrics": metrics,
	})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}

func (s *WebuiServer) handleOutdirViewLog(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	// TODO(b/533258244): move the raw logs concept into the [invocation.Invocation] interface?
	outdirInfo, ok := series.(*outdirInfo)
	if !ok {
		s.renderBuildViewError(http.StatusNotFound, "only outdirs are currently supported", w, r)
		return
	}

	tmpl, err := s.loadView("invocation_logs.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	allowedFilesMap := map[string]string{
		".siso_config":     ".siso_config%.0s",
		".siso_filegroups": ".siso_filegroups%.0s",
		"siso_localexec":   "siso_localexec.%s",
		"siso_output":      "siso_output.%s",
		"siso_trace.json":  "siso_trace.%s.json",
	}

	// Make sure this file is allowed.
	requestedFile := r.PathValue("file")
	revFileFormatter, ok := allowedFilesMap[requestedFile]
	if !ok {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("unknown file: %s", requestedFile), w, r)
		return
	}

	revID := r.PathValue("rev")
	// Webui identifies builds using build ID, but build ID is only known in siso_metrics.json.
	// We need to find the siso_metrics.json that contains the build ID to figure out the suffix.
	// Do this every page load because we don't know if another build has happened since past reload.
	// If this has happened, then the suffix would have changed.
	// (The alternative is either store all files in-memory, or refactor Webui to watch for files changing.)
	// TODO(b/533258244): decouple from outdirInfo?
	matches, err := filepath.Glob(filepath.Join(outdirInfo.path, "siso_metrics*.json"))
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to glob siso_metrics*.json: %v", err), w, r)
		return
	}
	buildSuffix := "unknown"
	// The loop to try reading every siso_metrics.json.
	// Use a buffer with small cap because these files are huge.
	// We only use the first build ID in the file which should be in the first few lines.
	buffer := make([]byte, 4096)
	for _, match := range matches {
		file, err := os.Open(match)
		if err != nil {
			continue
		}
		defer file.Close() // OK to ignore error, because we're just reading.
		n, err := file.Read(buffer)
		if err != nil && !errors.Is(err, io.EOF) {
			// TODO: log?
			continue
		}
		// Use n returned from file.Read so that we don't read more than the actual file.
		if strings.Contains(string(buffer[:n]), revID) {
			withoutExt := strings.TrimSuffix(filepath.Base(match), ".json")
			if withoutExt == "siso_metrics" {
				buildSuffix = ""
				break
			}
			buildSuffix = strings.TrimPrefix(withoutExt, "siso_metrics.")
			break
		}
	}
	if buildSuffix == "unknown" {
		if err != nil {
			s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to read siso_metrics*.json: %v", err), w, r)
			return
		}
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("couldn't find siso_metrics identifying build %s", revID), w, r)
		return
	}

	// Now that correct suffix is known then can get the right file.
	actualFile := requestedFile
	if buildSuffix != "" {
		actualFile = fmt.Sprintf(revFileFormatter, buildSuffix)
	}

	fileContents, err := os.ReadFile(filepath.Join(outdirInfo.path, actualFile))
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to open file: %v", err), w, r)
		return
	}

	if r.URL.Query().Get("raw") == "true" {
		w.Header().Add("Content-Type", "text/plain; charset=UTF-8")
		_, err := w.Write(fileContents)
		if err != nil {
			s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to write file contents: %v", err), w, r)
		}
		return
	}

	allowedFiles := slices.Sorted(maps.Keys(allowedFilesMap))

	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"allowedFiles": allowedFiles,
		"file":         requestedFile,
		"fileContents": string(fileContents),
	})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}

func (s *WebuiServer) handleInvocationAggregates(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	metrics := series.Get(r.PathValue("rev"))
	if metrics == nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("no metrics found for request %s", r.URL), w, r)
		return
	}

	aggregates := make(map[string]aggregateMetric)
	for _, m := range metrics.StepMetrics {
		// Aggregate by rule if exists otherwise action.
		aggregateBy := m.Action
		if len(m.Rule) > 0 {
			aggregateBy = m.Rule
		}
		entry, ok := aggregates[aggregateBy]
		if !ok {
			entry.AggregateBy = aggregateBy
		}
		entry.Count++
		entry.TotalUtime += m.Utime
		entry.TotalDuration += m.Duration
		entry.TotalWeightedDuration += m.WeightedDuration
		aggregates[aggregateBy] = entry
	}

	// Sort by utime descending.
	sortedAggregates := slices.Collect(maps.Values(aggregates))
	slices.SortFunc(sortedAggregates, func(a, b aggregateMetric) int {
		return cmp.Compare(b.TotalUtime, a.TotalUtime)
	})

	tmpl, err := s.loadView("invocation_aggregates.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"aggregates": sortedAggregates,
	})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}

func (s *WebuiServer) handleInvocationStepRecall(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	metrics := series.Get(r.PathValue("rev"))
	if metrics == nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("no metrics found for request %s", r.URL), w, r)
		return
	}

	tmpl, err := s.loadView("invocation_step_recall.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	metric, ok := metrics.stepByStepID[r.PathValue("id")]
	if !ok {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("stepID %s not found", r.PathValue("id")), w, r)
		return
	}

	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"stepID":        metric.StepID,
		"digest":        metric.Digest,
		"project":       r.FormValue("project"),
		"reapiInstance": r.FormValue("reapi_instance"),
	})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}

func (s *WebuiServer) handleInvocationViewStep(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	metrics := series.Get(r.PathValue("rev"))
	if metrics == nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("no metrics found for request %s", r.URL), w, r)
		return
	}

	tmpl, err := s.loadView("invocation_step.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	// Load the step.
	stepData, ok := metrics.stepByStepID[r.PathValue("id")]
	if !ok {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("stepID %s not found", r.PathValue("id")), w, r)
		return
	}

	// Find steps with the same output in other invocations under this series.
	// (A step could have multiple outputs, and we only log the first output as the output name.
	// So if it changes across builds it won't work. But it's expected to be stable for most builds.)
	inOtherRevs := make(map[string]build.StepMetric)
	for m := range series.All() {
		if step, ok := m.stepByOutput[stepData.Output()]; ok {
			inOtherRevs[m.Rev] = *step
		}
	}

	// We will only have special handling for a subset of stats, so provide the raw step for everything else.
	var stepRaw map[string]any
	asJSON, err := json.Marshal(stepData)
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to marshal metrics: %v", err), w, r)
	}
	err = json.Unmarshal(asJSON, &stepRaw)
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal metrics: %v", err), w, r)
	}

	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"step":        stepData,
		"stepRaw":     stepRaw,
		"inOtherRevs": inOtherRevs,
	})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}

func (s *WebuiServer) handleInvocationListSteps(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	metrics := series.Get(r.PathValue("rev"))
	if metrics == nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("no metrics found for request %s", r.URL), w, r)
		return
	}

	tmpl, err := s.loadView("invocation_steps.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	actionsWanted := r.URL.Query()["action"]
	rulesWanted := r.URL.Query()["rule"]
	view := r.URL.Query().Get("view")
	outputSearch := r.URL.Query().Get("q")

	sortBy := "ready"
	sortDescending := false
	sortSupported := true
	sortParam := r.URL.Query().Get("sort")
	if sortParamRe.MatchString(sortParam) {
		matches := sortParamRe.FindStringSubmatch(sortParam)
		sortBy = matches[sortParamRe.SubexpIndex("sortBy")]
		if matches[sortParamRe.SubexpIndex("order")] == "Dsc" {
			sortDescending = true
		}
	} else if len(sortParam) > 0 {
		s.renderBuildViewError(http.StatusBadRequest, fmt.Sprintf("invalid sort param: %s", sortParam), w, r)
		return
	}

	var filteredSteps []*build.StepMetric
	switch view {
	case "criticalPath":
		sortSupported = false
		// We assume the last step is on the critical path.
		// Build the critical path backwards then reverse it.
		critStepID := metrics.lastStepID
		for critStepID != "" {
			if step, ok := metrics.stepByStepID[critStepID]; ok {
				filteredSteps = append(filteredSteps, step)
				critStepID = step.PrevStepID
			} else {
				// TODO(b/349287453): add some sort of error to indicate prev step was not found
				break
			}
		}
		slices.Reverse(filteredSteps)
	default:
		for _, m := range metrics.StepMetrics {
			if len(actionsWanted) > 0 && !slices.Contains(actionsWanted, m.Action) {
				continue
			}
			if len(rulesWanted) > 0 && !slices.Contains(rulesWanted, m.Rule) {
				continue
			}
			if outputSearch != "" && !strings.Contains(m.Output(), outputSearch) {
				continue
			}
			if view == "localOnly" && !m.IsLocal {
				continue
			}
			filteredSteps = append(filteredSteps, m)
		}
		switch sortBy {
		case "ready":
			slices.SortFunc(filteredSteps, func(a, b *build.StepMetric) int {
				return cmp.Compare(a.Ready, b.Ready)
			})
		case "duration":
			slices.SortFunc(filteredSteps, func(a, b *build.StepMetric) int {
				return cmp.Compare(a.Duration, b.Duration)
			})
		case "completion":
			slices.SortFunc(filteredSteps, func(a, b *build.StepMetric) int {
				return cmp.Compare(a.Ready+a.Duration, b.Ready+b.Duration)
			})
		default:
			s.renderBuildViewError(http.StatusBadRequest, fmt.Sprintf("unknown sort column: %s", sortBy), w, r)
			return
		}
		if sortDescending {
			slices.Reverse(filteredSteps)
		}
	}

	itemsPerPage, err := strconv.Atoi(r.URL.Query().Get("items_per_page"))
	if err != nil {
		itemsPerPage = DefaultItemsPerPage
	}
	requestedPage, err := strconv.Atoi(r.URL.Query().Get("page"))
	if err != nil {
		requestedPage = 0
	}
	pageCount := len(filteredSteps) / itemsPerPage
	if len(filteredSteps)%itemsPerPage > 0 {
		pageCount++
	}

	pageFirst := 0
	pageLast := max(pageCount-1, 0)
	pageIndex := max(0, min(requestedPage, pageLast))
	pageNext := min(pageIndex+1, pageLast)
	pagePrev := max(0, pageIndex-1)
	itemsFirst := pageIndex * itemsPerPage
	itemsLast := max(0, min(itemsFirst+itemsPerPage, len(filteredSteps)))
	subset := filteredSteps[itemsFirst:itemsLast]

	data := map[string]any{
		"subset":           subset,
		"outputSearch":     outputSearch,
		"page":             requestedPage,
		"pageIndex":        pageIndex,
		"pageFirst":        pageFirst,
		"pageNext":         pageNext,
		"pagePrev":         pagePrev,
		"pageLast":         pageLast,
		"pageCount":        pageCount,
		"itemFirstLogical": itemsFirst + 1,
		"itemLastLogical":  itemsFirst + len(subset),
		"itemsLen":         len(filteredSteps),
		"actionCounts":     metrics.actionCounts,
		"ruleCounts":       metrics.ruleCounts,
		"buildDuration":    metrics.buildDuration,
		"sortSupported":    sortSupported,
	}
	err = s.renderBuildView(w, r, tmpl, data)
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}

func (s *WebuiServer) handleOutdirListTargets(w http.ResponseWriter, r *http.Request) {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("failed to load invocation(s) for %s: %v", r.URL, err), w, r)
		return
	}
	// TODO(b/533258244): introduce concept of invocation series that provides build.ninja file?
	outdirInfo, ok := series.(*outdirInfo)
	if !ok {
		s.renderBuildViewError(http.StatusNotFound, "this is not an outdir", w, r)
		return
	}

	target := r.PathValue("target")
	if target == "" {
		s.renderBuildViewError(http.StatusNotFound, "missing target", w, r)
		return
	}

	// Check manifest or manifest.stamp is newer to reload.
	// Don't continue if failed to stat manifest, but ignore if manifest.stamp failed to stat.
	// TODO(b/533258244): decouple from outdirInfo?
	stat, err := os.Stat(filepath.Join(outdirInfo.path, outdirInfo.manifestPath))
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to stat %s: %v", outdirInfo.manifestPath, err), w, r)
		return
	}
	buildNinjaMtime := stat.ModTime()
	stat, err = os.Stat(filepath.Join(outdirInfo.path, fmt.Sprintf("%s.stamp", outdirInfo.manifestPath)))
	if err == nil && stat.ModTime().After(buildNinjaMtime) {
		buildNinjaMtime = stat.ModTime()
	}
	outdirInfo.mu.Lock()
	defer outdirInfo.mu.Unlock()
	if buildNinjaMtime.After(outdirInfo.manifestMtime) {
		state := ninjautil.NewState()
		p := ninjautil.NewManifestParser(state)
		p.SetWd(outdirInfo.path)
		err = p.Load(r.Context(), outdirInfo.manifestPath)
		if err != nil {
			s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load manifest: %v", err), w, r)
			return
		}
		outdirInfo.ninjaState = state
		outdirInfo.manifestMtime = buildNinjaMtime
	}

	// Use cached *ninjautil.State to read info.
	nodes, err := outdirInfo.ninjaState.Targets([]string{target})
	if err != nil {
		if _, ok := errors.AsType[ninjautil.UnknownTargetError](err); ok {
			s.renderBuildViewError(http.StatusNotFound, fmt.Sprintf("target %s not found: %v", target, err), w, r)
			return
		}
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to get node for target %s: %v", target, err), w, r)
		return
	}
	if len(nodes) != 1 {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("unexpectedly got %d nodes querying target %s: %v", len(nodes), target, err), w, r)
		return
	}
	targetNode := nodes[0]
	var rule string
	var inputs []string
	var outputs []string
	inputType := make(map[string]string)
	if edge, ok := targetNode.InEdge(); ok {
		rule = edge.RuleName()
		for _, input := range edge.Inputs() {
			inputs = append(inputs, input.Path())
			// n = len(inputs), this is 2*O(n*2) check. but seems acceptable speed even for target="all"?
			if !slices.Contains(edge.Ins(), input) {
				if slices.Contains(edge.TriggerInputs(), input) {
					inputType[input.Path()] = "implicit"
				} else {
					inputType[input.Path()] = "order-only"
				}
			}
		}
	}
	for _, edge := range targetNode.OutEdges() {
		outs := edge.Outputs()
		if len(outs) == 0 {
			continue
		}
		outputs = append(outputs, outs[0].Path())
	}
	slices.Sort(inputs)
	slices.Sort(outputs)

	tmpl, err := s.loadView("ninja_targets.html")
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to load view: %s", err), w, r)
		return
	}

	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"target":    target,
		"rule":      rule,
		"inputs":    inputs,
		"inputType": inputType,
		"outputs":   outputs,
	})
	if err != nil {
		s.renderBuildViewError(http.StatusInternalServerError, fmt.Sprintf("failed to render view: %v", err), w, r)
	}
}
