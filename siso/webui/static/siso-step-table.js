// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

import { LitElement, html } from "lit";

export class SisoStepTable extends LitElement {
  static properties = {
    buildDuration: { type: Number, attribute: "build-duration" },
    baseUrl: { type: String, attribute: "base-url" },
    outdirRel: { type: String, attribute: "outdir-rel" },
    sortSupported: {
      type: Boolean,
      attribute: "sort-supported",
      converter: {
        fromAttribute: (value) => value !== "false" && value !== null,
        toAttribute: (value) => (value ? "" : null),
      },
    },
    steps: { type: Array },
  };

  createRenderRoot() {
    // Render in Light DOM so that global styles (style.css, theme.css, Material Symbols, fonts) apply seamlessly.
    return this;
  }

  constructor() {
    super();
    this.buildDuration = 0;
    this.baseUrl = "";
    this.outdirRel = "";
    this.sortSupported = true;
    this.steps = [];
  }

  connectedCallback() {
    super.connectedCallback();
    this.loadSteps();
  }

  willUpdate(changedProperties) {
    super.willUpdate(changedProperties);
    this.loadSteps();
  }

  loadSteps() {
    if (this.steps && this.steps.length > 0) return;
    const script = this.querySelector('script[type="application/json"]');
    if (script && script.textContent) {
      try {
        this.steps = JSON.parse(script.textContent);
      } catch (e) {
        console.error("Failed to parse step table JSON:", e);
      }
    }
  }

  formatIntervalMetricTimestamp(ns) {
    if (ns == null || isNaN(ns)) return " 0m00.00s";
    const totalMs = ns / 1e6;
    const minute = Math.floor(totalMs / 60000);
    const second = Math.floor((totalMs % 60000) / 1000);
    const ms = Math.round((totalMs % 1000) / 10);
    return `${minute}m${String(second).padStart(2, "0")}.${String(ms).padStart(2, "0")}s`;
  }

  formatIntervalMetricHuman(ns) {
    if (ns == null || isNaN(ns)) return "0.00s";
    const ms = ns / 1e6;
    if (ms > 10) {
      const totalSec = ms / 1000;
      const mins = Math.floor(totalSec / 60);
      const secs = totalSec % 60;
      if (mins > 0) {
        return `${mins}m${secs < 10 ? "0" : ""}${secs.toFixed(2)}s`;
      }
      return `${secs.toFixed(2)}s`;
    } else {
      const us = (ns % 1e6) / 1000;
      return `${Math.floor(ms)}.${String(Math.round(us / 10)).padStart(2, "0")}ms`;
    }
  }

  toggleSort(col) {
    const url = new URL(window.location.href);
    const currentSort = url.searchParams.get("sort") || "readyAsc";
    let nextSort;
    if (currentSort === col + "Asc") {
      nextSort = col + "Dsc";
    } else {
      nextSort = col + "Asc";
    }
    url.searchParams.set("sort", nextSort);
    window.location.href = url.toString();
  }

  renderTimeline(step) {
    const buildDuration = this.buildDuration || 1;
    const ready = step.ready_nanos || 0;
    const start = step.start_nanos || 0;
    const duration = step.duration_nanos || 0;
    const actionStart = step.action_start_nanos || 0;
    const actionEnd = step.action_end_nanos || 0;
    const run = step.run_nanos || 0;
    const execStart = step.exec_start_nanos;
    const execTime = step.exec_nanos || 0;

    const scale = (val) => (val / buildDuration) * 100;

    return html`
      <svg height="20" class="step-timeline-bar" width="100%">
        <rect
          x="${scale(ready)}%"
          y="0"
          width="${scale(start)}%"
          height="20"
          style="fill: rgb(50, 176, 73)"
        />
        <rect
          x="${scale(ready + start)}%"
          y="0"
          width="${scale(duration)}%"
          height="20"
          style="fill: rgb(48, 48, 48)"
        />
        <rect
          x="${scale(actionStart)}%"
          y="0"
          width="${scale(Math.max(0, actionEnd - actionStart))}%"
          height="20"
          style="fill: rgb(251, 255, 0)"
        />
        <rect
          x="${scale(actionStart)}%"
          y="0"
          width="${scale(run)}%"
          height="20"
          style="fill: rgb(223, 134, 30)"
        />
        ${execStart
          ? html`
              <rect
                x="${scale(execStart)}%"
                y="0"
                width="${scale(execTime)}%"
                height="20"
                style="fill: rgb(134, 39, 222)"
              />
            `
          : ""}
      </svg>
    `;
  }

  render() {
    const sortParam =
      new URLSearchParams(window.location.search).get("sort") || "readyAsc";
    const isReadyAsc = sortParam === "readyAsc";
    const isReadyDsc = sortParam === "readyDsc";
    const isDurationAsc = sortParam === "durationAsc";
    const isDurationDsc = sortParam === "durationDsc";
    const isCompletionAsc = sortParam === "completionAsc";
    const isCompletionDsc = sortParam === "completionDsc";

    return html`
      <div class="data-table-container">
        <table>
          <thead>
            <tr>
              <th scope="col">
                Output
                <div class="subrow">Rule</div>
              </th>
              <th class="thin-column remote-indicator" scope="col">Remote</th>
              ${this.sortSupported
                ? html`
                    <th class="timestamp-column" scope="col">
                      <md-text-button
                        class="sort-header"
                        trailing-icon
                        @click=${() => this.toggleSort("ready")}
                      >
                        Ready
                        ${isReadyAsc
                          ? html`<md-icon slot="icon">arrow_upward</md-icon>`
                          : ""}
                        ${isReadyDsc
                          ? html`<md-icon slot="icon">arrow_downward</md-icon>`
                          : ""}
                      </md-text-button>
                    </th>
                    <th class="timestamp-column" scope="col">
                      <md-text-button
                        class="sort-header"
                        trailing-icon
                        @click=${() => this.toggleSort("duration")}
                      >
                        Duration
                        ${isDurationAsc
                          ? html`<md-icon slot="icon">arrow_upward</md-icon>`
                          : ""}
                        ${isDurationDsc
                          ? html`<md-icon slot="icon">arrow_downward</md-icon>`
                          : ""}
                      </md-text-button>
                    </th>
                    <th class="timestamp-column" scope="col">
                      <md-text-button
                        class="sort-header"
                        trailing-icon
                        @click=${() => this.toggleSort("completion")}
                      >
                        Completion
                        ${isCompletionAsc
                          ? html`<md-icon slot="icon">arrow_upward</md-icon>`
                          : ""}
                        ${isCompletionDsc
                          ? html`<md-icon slot="icon">arrow_downward</md-icon>`
                          : ""}
                      </md-text-button>
                    </th>
                  `
                : html`
                    <th scope="col">Ready</th>
                    <th scope="col">Duration</th>
                    <th scope="col">Completion</th>
                  `}
              <th class="timeline-column" scope="col">Timeline</th>
            </tr>
          </thead>
          <tbody id="data-table-body">
            ${this.steps.map((step) => {
              const output = step.outputs?.[0] || "";
              const trimmedOutput =
                this.outdirRel && output.startsWith(this.outdirRel + "/")
                  ? output.slice(this.outdirRel.length + 1)
                  : output;
              const stepUrl = this.baseUrl
                ? `${this.baseUrl}/steps/${encodeURIComponent(step.step_id || "")}/`
                : "#";
              const isLocal = step.is_local;
              const isCached = step.cached;
              const isNoExec = step.no_exec;

              return html`
                <tr>
                  <th scope="row">
                    <a href="${stepUrl}">${trimmedOutput}</a>
                    <div class="subrow">
                      <span class="step-rule">${step.rule || ""}</span>
                      <span class="step-action">${step.action || ""}</span>
                    </div>
                  </th>
                  <td class="remote-indicator">
                    ${isLocal
                      ? ""
                      : !isCached && !isNoExec
                        ? html`
                            <md-icon
                              class="icon-cache-miss"
                              title="Remote Execution"
                              >cloud</md-icon
                            >
                          `
                        : isCached
                          ? html`
                              <md-icon class="icon-cache-hit" title="Cache Hit"
                                >cloud_done</md-icon
                              >
                            `
                          : ""}
                  </td>
                  <td class="timestamp">
                    ${this.formatIntervalMetricTimestamp(step.ready_nanos || 0)}
                  </td>
                  <td class="timestamp">
                    ${this.formatIntervalMetricHuman(step.duration_nanos || 0)}
                  </td>
                  <td class="timestamp">
                    ${this.formatIntervalMetricTimestamp(
                      (step.ready_nanos || 0) + (step.duration_nanos || 0),
                    )}
                  </td>
                  <td>${this.renderTimeline(step)}</td>
                </tr>
              `;
            })}
          </tbody>
        </table>
      </div>
    `;
  }
}

customElements.define("siso-step-table", SisoStepTable);
