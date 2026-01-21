// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package log provides a `slog.Handler` implementation that prints pretty log
// output to the console.
package log

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"strings"
	"sync"
)

const (
	timeFormat = "15:04"

	reset  = "\033[0m"
	red    = "\033[31m"
	green  = "\033[32m"
	yellow = "\033[33m"
	blue   = "\033[34m"
	purple = "\033[35m"
	cyan   = "\033[36m"
	white  = "\033[37m"
	gray   = "\033[90m"
)

// PrettyHandler is a `slog.Handler` that writes pretty log output to `w`.
type PrettyHandler struct {
	opts              slog.HandlerOptions
	w                 io.Writer
	mu                *sync.Mutex
	preformattedAttrs []byte
	groupPrefix       string
}

// NewPrettyHandler creates a new `PrettyHandler` that writes to `w`.
func NewPrettyHandler(w io.Writer, opts *slog.HandlerOptions) *PrettyHandler {
	if opts == nil {
		opts = &slog.HandlerOptions{}
	}
	return &PrettyHandler{
		opts: *opts,
		w:    w,
		mu:   &sync.Mutex{},
	}
}

// Enabled reports whether the handler handles records at the given level.
// We ignore records whose level is lower than `level`.
func (h *PrettyHandler) Enabled(ctx context.Context, level slog.Level) bool {
	minLevel := slog.LevelInfo
	if h.opts.Level != nil {
		minLevel = h.opts.Level.Level()
	}
	return level >= minLevel
}

// Handle formats the message contained in record `r` so that it looks nice when printed to the
// console. The formatted message is written to the Writer that was specified when creating the
// handler.
func (h *PrettyHandler) Handle(ctx context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	level := r.Level.String()
	levelColor := reset

	switch r.Level {
	case slog.LevelDebug:
		level = "DBG"
		levelColor = white
	case slog.LevelInfo:
		level = "INF"
		levelColor = green
	case slog.LevelWarn:
		level = "WRN"
		levelColor = yellow
	case slog.LevelError:
		level = "ERR"
		levelColor = red
	}

	timestamp := r.Time.Format(timeFormat)

	// Format: Time Level Message Key=Value ...
	// Example: 15:41 INF Starting listener listen=:8080 pid=37556
	_, err := fmt.Fprintf(h.w, "%s%s%s %s%s%s %s",
		gray, timestamp, reset,
		levelColor, level, reset,
		r.Message,
	)
	if err != nil {
		return err
	}

	// Write preformatted attributes
	if len(h.preformattedAttrs) > 0 {
		_, err = h.w.Write(h.preformattedAttrs)
		if err != nil {
			return err
		}
	}

	// Process record attributes
	r.Attrs(func(attr slog.Attr) bool {
		fmt.Fprint(h.w, printAttr(attr, h.groupPrefix))
		return true
	})

	_, err = fmt.Fprintln(h.w)
	return err
}

func printAttr(attr slog.Attr, groupPrefix string) string {
	if attr.Key == "" {
		return ""
	}

	key := groupPrefix + attr.Key
	val := attr.Value.Resolve()
	valueColor := reset
	if key == "error" {
		valueColor = red
	}

	valStr := fmt.Sprintf("%+v", val.Any())
	return fmt.Sprintf(" %s%s%s=%s%s%s", cyan, key, reset, valueColor, valStr, reset)
}

// WithAttrs returns a new `PrettyHandler` whose attributes consist of both the receiver's
// attributes and the arguments.
func (h *PrettyHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	// Formatted attributes are appended to preformattedAttrs
	var sb strings.Builder
	for _, attr := range attrs {
		sb.WriteString(printAttr(attr, h.groupPrefix))
	}

	return &PrettyHandler{
		opts:              h.opts,
		w:                 h.w,
		mu:                h.mu,
		preformattedAttrs: slices.Concat(h.preformattedAttrs, []byte(sb.String())),
		groupPrefix:       h.groupPrefix,
	}
}

// WithGroup returns a new `PrettyHandler` with the given group appended to the receiver's existing
// groups.
func (h *PrettyHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	return &PrettyHandler{
		opts:              h.opts,
		w:                 h.w,
		mu:                h.mu,
		preformattedAttrs: h.preformattedAttrs,
		// The groupPrefix needs to have a trailing dot, because printAttr then later
		// simply appends the name of the attribute key to it. This gives a nice looking
		// "foo.bar.attrname" result.
		groupPrefix: h.groupPrefix + name + ".",
	}
}
