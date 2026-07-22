// Copyright 2024 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package webui implements siso webui.
package webui

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"text/template"
	"time"

	"go.chromium.org/build/siso/build"
	"go.chromium.org/build/siso/build/ninjabuild"
	mwc "go.chromium.org/build/siso/third_party/material_web_components"
	"go.chromium.org/build/siso/webui/invocation"
)

//go:embed templates/*.html static/*.css static/*.js
var content embed.FS

var (
	combinedCSSPathRe = regexp.MustCompile(`/combined.(\d+).css`)

	// sisoStateHeuristics lists files that when present inside an output subdirectory under 'out/',
	// strongly indicate that the subdirectory represents an active build configuration (rather than
	// flat/intermediate outputs like 'soong' or 'target' under AOSP's 'out/' folder).
	sisoStateHeuristics = []string{
		"siso_metrics.json",
		".siso_deps",
		"build.ninja",
	}
	// baseFunctions provides global functions to the HTML template files.
	baseFunctions = template.FuncMap{
		// outdirEscape escapes all special characters for safe inclusion in a URL path
		// segment, except slashes which are assumed to be part of the outdir's path and
		// hence left as-is.
		"outdirEscape": func(s string) string {
			return strings.ReplaceAll(url.PathEscape(s), "%2F", "/")
		},
		"pathEscape": func(s string) string {
			return url.PathEscape(s)
		},
		"urlPathEq": func(url *url.URL, path string) bool {
			return url.Path == path
		},
		"urlPathHasPrefix": func(url *url.URL, prefix string) bool {
			return strings.HasPrefix(url.Path, prefix)
		},
		"urlParamGet": func(url *url.URL, key string) string {
			return url.Query().Get(key)
		},
		"urlParamEq": func(url *url.URL, key, value string) bool {
			return url.Query().Get(key) == value
		},
		"urlHasParam": func(url *url.URL, key, value string) bool {
			return slices.Contains(url.Query()[key], value)
		},
		"urlParamIsSet": func(url *url.URL, key string) bool {
			return len(url.Query()[key]) > 0
		},
		"urlParamReplace": func(url *url.URL, key, value string) *url.URL {
			query := url.Query()
			query.Set(key, value)
			url.RawQuery = query.Encode()
			return url
		},
		"divIntervalsScaled": func(a, b build.IntervalMetric, scale float64) float64 {
			return float64(a) / float64(b) * scale
		},
		"addIntervals": func(a, b build.IntervalMetric) build.IntervalMetric {
			return a + b
		},
		"subIntervals": func(a, b build.IntervalMetric) build.IntervalMetric {
			return a - b
		},
		"trimPrefix": func(s, prefix string) string {
			return strings.TrimPrefix(s, prefix)
		},
		"formatIntervalMetricTimestamp": func(i build.IntervalMetric) string {
			d := time.Duration(i)
			minute := int(d.Minutes())
			second := int(d.Seconds()) % 60
			ms := d.Milliseconds() % 1000
			// Reduce precision to 2 digits
			ms = int64(math.Round(float64(ms) / 10))
			return fmt.Sprintf("%2dm%02d.%02ds", minute, second, ms)
		},
		"formatIntervalMetricHuman": func(i build.IntervalMetric) string {
			var sb strings.Builder
			d := time.Duration(i)
			ms := d.Milliseconds() % 1000
			if ms > 10 {
				d = d.Round(10 * time.Millisecond)
				mins := d.Truncate(1 * time.Minute)
				d = d - mins
				if mins > 0 {
					fmt.Fprintf(&sb, "%s", strings.TrimSuffix(mins.String(), "0s"))
					if d < 10*time.Second {
						fmt.Fprint(&sb, "0")
					}
				}
				fmt.Fprintf(&sb, "%2.02fs", d.Seconds())
			} else {
				d = d.Round(10 * time.Microsecond)
				us := d.Microseconds() % 1000
				// Reduce precision to 2 digits
				us = int64(math.Round(float64(us) / 10))
				fmt.Fprintf(&sb, "%d.%02dms", ms, us)
			}
			return sb.String()
		},
		"buildTimeHumanReadable": func(m *buildMetrics) (string, error) {
			local, err := time.LoadLocation("Local")
			if err != nil {
				return "", fmt.Errorf("couldn't get local time location")
			}
			now := time.Now()
			buildTimeLocal := m.Mtime.In(local)
			nowY, nowM, nowD := now.Date()
			buildY, buildM, buildD := buildTimeLocal.Date()
			if buildY == nowY && buildM == nowM && buildD == nowD {
				return buildTimeLocal.Format("Today 15:04"), nil
			} else if buildY == nowY && buildM == nowM && buildD == (nowD-1) {
				return buildTimeLocal.Format("Yesterday 15:04"), nil
			} else if buildY == nowY ||
				(buildY == nowY-1 && buildM > nowM) {
				return buildTimeLocal.Format("Jan _2 15:04"), nil
			}
			return buildTimeLocal.Format("Jan _2 2006 15:04"), nil
		},
		"timeRFC3339": func(t time.Time) string {
			return t.Format(time.RFC3339)
		},
	}
	sisoMetricsRe = regexp.MustCompile(`siso_metrics.(\d+).json`)
	sortParamRe   = regexp.MustCompile(`^(?P<sortBy>[a-z]+?)(?P<order>Asc|Dsc)$`)
)

type WebuiServer struct {
	sisoVersion      string
	localDevelopment bool
	port             int
	staticFS         fs.FS
	sseServer        *sseServer
	workspaceRoot    string
	defaultOutdir    string
	outdirProvider   outdirProvider
	knownOutdirs     []string // TODO: fold into outdirProvider
	uploadedMetrics  metricsFileProvider
	runbuildState

	cssMu               sync.RWMutex
	combinedCSS         string
	combinedCSSChecksum uint32

	templatesMu sync.RWMutex
	templates   map[string]*template.Template
}

type runningStepInfo struct {
	stepOut  string
	stepType string
	started  time.Time
}

// ErrWorkspaceNotExist represents error when workspace was not found.
type ErrWorkspaceNotExist struct {
	err error
}

func (f ErrWorkspaceNotExist) Unwrap() error {
	return f.err
}

func (f ErrWorkspaceNotExist) Error() string {
	return fmt.Sprintf("failed to find workspace: %v", f.err)
}

// ErrManifestNotExist represents error when build manifest was not found.
type ErrManifestNotExist struct {
	outdirPath   string
	manifestPath string
}

func (f ErrManifestNotExist) Error() string {
	return fmt.Sprintf("%s not found in %s", f.manifestPath, f.outdirPath)
}

// loadView lazy-parses a view once, or parses every time if in local development mode.
func (s *WebuiServer) loadView(view string) (*template.Template, error) {
	s.templatesMu.RLock()
	tmpl, ok := s.templates[view]
	s.templatesMu.RUnlock()
	if !s.localDevelopment && ok {
		return tmpl, nil
	}
	templatesFS, err := fs.Sub(s.staticFS, "templates")
	if err != nil {
		return nil, fmt.Errorf("templates not found: %w", err)
	}
	tmpl, err = template.New("").Funcs(baseFunctions).ParseFS(templatesFS, "webui_base.html", view)
	if err != nil {
		return nil, fmt.Errorf("failed to parse view: %w", err)
	}
	if s.localDevelopment {
		return tmpl, nil
	}
	s.templatesMu.Lock()
	defer s.templatesMu.Unlock()
	if cachedTmpl, ok := s.templates[view]; ok {
		return cachedTmpl, nil
	}
	s.templates[view] = tmpl
	return tmpl, nil
}

// ensureCSS lazy-loads the global stylesheet, or loads every time if in local development mode.
func (s *WebuiServer) ensureCSS() error {
	// Fast-path: Check if CSS is already cached. We use a read-lock here
	// to avoid blocking other concurrent requests in production mode.
	s.cssMu.RLock()
	hasCSS := s.combinedCSS != ""
	s.cssMu.RUnlock()
	if !s.localDevelopment && hasCSS {
		return nil
	}
	sb := strings.Builder{}
	for _, stylesheet := range []string{
		"static/light.css",
		"static/light-hc.css",
		"static/light-mc.css",
		"static/dark.css",
		"static/dark-hc.css",
		"static/dark-mc.css",
		"static/style.css",
	} {
		f, err := s.staticFS.Open(stylesheet)
		if err != nil {
			return fmt.Errorf("failed to open %q: %w", stylesheet, err)
		}
		data, err := io.ReadAll(f)
		if err != nil {
			return fmt.Errorf("failed to read %q: %w", stylesheet, err)
		}
		sb.Write(data)
		sb.WriteByte('\n')
	}
	// Slow-path: Lock for writing to update the cache.
	s.cssMu.Lock()
	defer s.cssMu.Unlock()
	// Double-check: Another concurrent request might have populated the cache
	// while we were waiting for the write lock.
	if !s.localDevelopment && s.combinedCSS != "" {
		return nil
	}
	s.combinedCSS = sb.String()
	s.combinedCSSChecksum = crc32.ChecksumIEEE([]byte(s.combinedCSS))
	return nil
}

// baseURLFromRequest gets the base URL from context.
// This is a HARDCODED assumption that siso webui only has routes that start with outdir.
func outdirBaseURL(r *http.Request) string {
	return fmt.Sprintf("/%s/%s", url.PathEscape(r.PathValue("outroot")), url.PathEscape(r.PathValue("outsub")))
}

// renderBuildView renders a build-related view.
// TODO(b/361703735): return data instead of write to response writer? https://chromium-review.googlesource.com/c/infra/infra/+/5803123/comment/4ce69ada_31730349/
func (s *WebuiServer) renderBuildView(wr http.ResponseWriter, r *http.Request, tmpl *template.Template, data map[string]any) error {
	series, err := s.invocationSeriesFor(r)
	if err != nil {
		return fmt.Errorf("failed to load invocation(s) for %s: %v", r.URL, err)
	}
	rev := r.PathValue("rev")
	// TODO(b/533258244): remove hardcoded dependency that we're either viewing
	// an outdir or an uploaded metrics file and it can't be anything else?
	switch g := series.(type) {
	case metricsFileInfo:
		data["viewingUploaded"] = true
	case *outdirInfo:
		if rev == "" {
			rev = g.latestRevID
		}
		outdirAbbrev := g.path
		// Showing the full path is too long in the webui so abbreviate home dir to ~.
		// TODO(b/361703735): refactor https://chromium-review.googlesource.com/c/infra/infra/+/5804478/comment/dcfb372d_f21e4cc5/
		if home, err := os.UserHomeDir(); err == nil {
			outdirAbbrev = strings.Replace(outdirAbbrev, home, "~", 1)
		}
		data["outdirAbbrev"] = outdirAbbrev
		data["outdirRel"] = g.pathRel
		data["revs"] = g.metrics
	}
	data["knownOutdirs"] = s.knownOutdirs
	data["versionID"] = s.sisoVersion
	data["currentURL"] = r.URL
	data["currentRev"] = rev
	data["outdirBaseURL"] = outdirBaseURL(r)
	if rev != "" {
		data["outdirRevBaseURL"] = fmt.Sprintf("%s/builds/%s", data["outdirBaseURL"], rev)
	}
	err = s.ensureCSS()
	if err != nil {
		return fmt.Errorf("failed to ensure CSS: %w", err)
	}
	// Use checksum for CSS for cache busting.
	s.cssMu.RLock()
	checksum := s.combinedCSSChecksum
	s.cssMu.RUnlock()
	data["combinedCSSPath"] = fmt.Sprintf("/combined.%d.css", checksum)
	err = tmpl.ExecuteTemplate(wr, "base", data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}
	return nil
}

// renderBuildViewError renders a build-related error.
func (s *WebuiServer) renderBuildViewError(status int, message string, w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(status)
	tmpl, err := s.loadView("_error.html")
	if err != nil {
		fmt.Fprintf(w, "failed to load error view: %s\n", err)
		return
	}
	err = s.renderBuildView(w, r, tmpl, map[string]any{
		"errorTitle":   http.StatusText(status),
		"errorMessage": message,
	})
	if err != nil {
		fmt.Fprintf(w, "failed to render error view: %s\n", err)
	}
}

// ServerConfig holds configuration for NewServer.
type ServerConfig struct {
	Version          string
	LocalDevelopment bool
	Port             int
	OutDir           ninjabuild.DirFlag
	ManifestPath     string
}

// NewServer inits a webui server.
func NewServer(ctx context.Context, cfg ServerConfig) (*WebuiServer, error) {
	_, workspaceRoot, outDir, err := ninjabuild.InitDir(ctx, cfg.OutDir)
	if err != nil {
		return nil, &ErrWorkspaceNotExist{err}
	}
	defaultOutdir := filepath.ToSlash(outDir)
	s := WebuiServer{
		sisoVersion:      cfg.Version,
		localDevelopment: cfg.LocalDevelopment,
		staticFS:         fs.FS(content),
		sseServer:        newSseServer(),
		workspaceRoot:    workspaceRoot,
		defaultOutdir:    defaultOutdir,
		outdirProvider:   makeOutdirProvider(workspaceRoot, cfg.ManifestPath, defaultOutdir),
		uploadedMetrics:  makeMetricsFileProvider(),
		port:             cfg.Port,
		templates:        make(map[string]*template.Template),
	}

	if cfg.LocalDevelopment {
		s.staticFS = os.DirFS("webui/")
	}

	// Preload default outdir.
	_, err = s.outdirProvider.Get(outDir)
	if err != nil {
		return nil, fmt.Errorf("failed to preload outdir: %w", err)
	}

	// Find other outdirs.
	matches, err := filepath.Glob(filepath.Join(s.workspaceRoot, "out*/*"))
	if err != nil {
		return nil, fmt.Errorf("failed to glob %s: %w", s.workspaceRoot, err)
	}
	for _, match := range matches {
		m, err := os.Stat(match)
		if err != nil {
			return nil, fmt.Errorf("failed to stat outdir %s: %w", match, err)
		}
		if m.IsDir() {
			// Skip directories that do not heuristically look like build configuration directories.
			entries, err := os.ReadDir(match)
			if err != nil {
				continue
			}
			if !slices.ContainsFunc(entries, func(entry fs.DirEntry) bool {
				return slices.Contains(sisoStateHeuristics, entry.Name())
			}) {
				continue
			}
			outsub, err := filepath.Rel(s.workspaceRoot, match)
			if err != nil {
				return nil, fmt.Errorf("failed to make %s workspace-root relative: %w", match, err)
			}
			s.knownOutdirs = append(s.knownOutdirs, filepath.ToSlash(outsub))
		}
	}

	return &s, nil
}

func (s *WebuiServer) LoadStandaloneMetrics(metricsPath string) error {
	_, err := s.uploadedMetrics.Get(metricsPath)
	return err
}

func (s *WebuiServer) invocationSeriesFor(r *http.Request) (invocation.Series[*buildMetrics], error) {
	if r.PathValue("outroot") == "uploads" && r.PathValue("outsub") == "view" {
		s.uploadedMetrics.mu.Lock()
		defer s.uploadedMetrics.mu.Unlock()
		for _, m := range s.uploadedMetrics.files {
			if m.metrics.Rev == r.PathValue("rev") {
				return m, nil
			}
		}
		return nil, fmt.Errorf("not found")
		// TODO: move into uploaded.go?
	}

	outroot := r.PathValue("outroot")
	outsub := r.PathValue("outsub")
	path := filepath.Join(outroot, outsub)
	if outsub == flatOutsub {
		path = outroot
	}
	return s.outdirProvider.Get(path)
}

func (s *WebuiServer) staticFileHandler(h http.Handler) http.Handler {
	if s.localDevelopment {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Cache-Control", "max-age=86400, private")
		h.ServeHTTP(w, r)
	})
}

func (s *WebuiServer) mux() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/events/", s.sseServer)

	// Subrouter for all outdir related URLs.
	// This is set up on a separate mux because it's too generic and would otherwise cause panic:
	//     /static/ and /{outroot}/{outsub}/ both match some paths, like "/static/outsub/".
	//     But neither is more specific than the other.
	//     /static/ matches "/static/", but /{outroot}/{outsub}/ doesn't.
	//     /{outroot}/{outsub}/ matches "/outroot/outsub/", but /static/ doesn't.
	outdirRouter := http.NewServeMux()
	outdirRouter.HandleFunc("/{outroot}/{outsub}/", s.handleInvocationSeriesRoot)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/builds/{rev}/logs/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("%s/builds/%s/logs/.siso_config", outdirBaseURL(r), url.PathEscape(r.PathValue("rev"))), http.StatusTemporaryRedirect)
	})
	outdirRouter.HandleFunc("/{outroot}/{outsub}/targets/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, fmt.Sprintf("%s/targets/all/", outdirBaseURL(r)), http.StatusTemporaryRedirect)
	})
	outdirRouter.HandleFunc("/{outroot}/{outsub}/runbuild/", s.handleRunbuildGet)
	outdirRouter.HandleFunc("POST /{outroot}/{outsub}/runbuild/", s.handleRunbuildPost)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/reload", s.handleOutdirReload)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/watch/", s.handleOutdirWatch)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/builds/{rev}/logs/{file}", s.handleOutdirViewLog)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/builds/{rev}/aggregates/", s.handleInvocationAggregates)
	outdirRouter.HandleFunc("POST /{outroot}/{outsub}/builds/{rev}/steps/{id}/recall/", s.handleInvocationDoRecall)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/builds/{rev}/steps/{id}/", s.handleInvocationViewStep)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/builds/{rev}/steps/", s.handleInvocationListSteps)
	outdirRouter.HandleFunc("/{outroot}/{outsub}/targets/{target}/", s.handleOutdirListTargets)

	// Handlers for uploaded metrics.
	// We define these explicitly by catching all URLs starting with /uploads/view/, and defining a subset
	// of the /{outroot}/{outsub}/ routes so that only supported routes will load, and all others will 404.
	// (For example, it doesn't make sense to support /runbuild/ or /reload/ for uploaded metrics.)
	// Siso webui was created assuming /{outroot}/{outsub}/ with on-disk metrics rather than uploaded.
	// Furthermore, we also have hardcoded links built around this URL structure.
	// It would probably be more ideal to have just "/uploads/{rev}/builds/steps/", but it would require more refactoring.
	uploadsRouter := http.NewServeMux()
	uploadsRouter.HandleFunc("/uploads/view/", s.handleInvocationSeriesRoot)
	uploadsRouter.HandleFunc("/uploads/view/builds/{rev}/aggregates/", s.handleInvocationAggregates)
	uploadsRouter.HandleFunc("POST /uploads/view/builds/{rev}/steps/{id}/recall/", s.handleInvocationDoRecall)
	uploadsRouter.HandleFunc("/uploads/view/builds/{rev}/steps/{id}/", s.handleInvocationViewStep)
	uploadsRouter.HandleFunc("/uploads/view/builds/{rev}/steps/", s.handleInvocationListSteps)
	mux.HandleFunc("/uploads/view/", func(w http.ResponseWriter, r *http.Request) {
		// This is how we hack around the hardcoded assumption that URLs are /{outroot}/{outsub}/.
		// Common code paths will then have checks to handle this special case.
		r.SetPathValue("outroot", "uploads")
		r.SetPathValue("outsub", "view")
		uploadsRouter.ServeHTTP(w, r)
	})

	// Default catch-all handler.
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Redirect root to default outdir.
		if r.URL.Path == "/" {
			http.Redirect(w, r, fmt.Sprintf("/%s/", s.defaultOutdir), http.StatusTemporaryRedirect)
			return
		}

		// Serve the combined CSS via the catch-all handler.
		// The http.Handle wildcards that were introduced in go1.22
		// https://go.dev/blog/routing-enhancements unfortunately don't
		// support "/combined.{foo}.css" which is preferable over "/combined.css?v=123"
		// https://css-tricks.com/strategies-for-cache-busting-css/
		// (We don't actually validate the checksum, it's only for cache busting)
		if combinedCSSPathRe.MatchString(r.URL.Path) {
			w.Header().Add("Content-Type", "text/css; charset=UTF-8")
			w.Header().Add("Cache-Control", "max-age=86400, private") // 1 day
			s.cssMu.RLock()
			css := s.combinedCSS
			s.cssMu.RUnlock()
			w.Write([]byte(css))
			return
		}

		// Delegate all other requests to the outdir subrouter.
		outdirRouter.ServeHTTP(w, r)
	})

	mux.Handle("/static/", s.staticFileHandler(http.FileServerFS(s.staticFS)))

	// Serve third party JS. No other third party libraries right now, so just serve Material Design node_modules root.
	mux.Handle("/third_party/", http.StripPrefix("/third_party/", s.staticFileHandler(http.FileServerFS(mwc.NodeModulesFS))))

	return mux
}

// Serve serves the webui with the current configuration.
func (s *WebuiServer) Serve() int {
	s.sseServer.Start()
	fmt.Printf("listening on http://localhost:%d/...\n", s.port)
	// Hack for now to make loading external siso_metrics.json more usable, until we can have the homepage automatically show "here's all the loaded siso_metrics"
	// TODO(b/533258244): now that we have the concept of invocation providers, this should become feasible.
	if len(s.uploadedMetrics.files) > 0 {
		fmt.Printf("for provided siso_metrics.json:\n")
		for _, file := range s.uploadedMetrics.files {
			fmt.Printf("- http://localhost:%d/uploads/view/builds/%s/steps/\n", s.port, file.metrics.ID())
		}
	}
	err := http.ListenAndServe(fmt.Sprintf(":%d", s.port), s.mux())
	if errors.Is(err, http.ErrServerClosed) {
		fmt.Printf("server closed\n")
	} else if err != nil {
		fmt.Printf("error starting server: %s\n", err)
		return 1
	}
	return 0
}
