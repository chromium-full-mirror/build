// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

import { LitElement, html } from 'lit';
import { filterSteps, sortSteps } from './step-transforms.js';
import { stepQueryState } from './step-query-state.js';
import { StepSidebarBinder } from './step-sidebar-binding.js';

export class SisoStepTable extends LitElement {
  static properties = {
    endpoint: { type: String },
    baseUrl: { type: String, attribute: 'base-url' },
    sidebar: { type: String },
    _loading: { state: true },
    _error: { state: true },
    _data: { state: true },
  };

  createRenderRoot() {
    // Render in Light DOM so that global styles (style.css, theme.css, Material Symbols, fonts) apply seamlessly.
    return this;
  }

  constructor(queryState = stepQueryState) {
    super();
    this.endpoint = '';
    this.baseUrl = '';
    this.sidebar = '#page-sidebar';
    this._loading = false;
    this._error = '';
    this._data = null;

    this.queryState = queryState;
    this._sidebarBinder = new StepSidebarBinder(this.queryState, () => this.sortedSteps.length);
    this._onQueryChange = () => this.requestUpdate();
  }

  getSidebarElement() {
    if (this.sidebar instanceof HTMLElement) {
      return this.sidebar;
    }
    const selector = typeof this.sidebar === 'string' && this.sidebar ? this.sidebar : '#page-sidebar';
    return document.querySelector(selector);
  }

  _bindSidebar() {
    const el = this.getSidebarElement();
    if (el) {
      this._sidebarBinder.bind(el);
    }
  }

  connectedCallback() {
    super.connectedCallback();
    this.queryState.addEventListener('change', this._onQueryChange);
    this.queryState.initFromURL();
    this._bindSidebar();
    this.fetchData();
  }

  disconnectedCallback() {
    super.disconnectedCallback();
    this.queryState.removeEventListener('change', this._onQueryChange);
    this._sidebarBinder.unbind();
  }

  /**
   * Formats an interval in nanoseconds to "XmYY.ZZs".
   * @param {number} ns
   * @return {string}
   */
  formatIntervalMetricTimestamp(ns) {
    if (ns === null || ns === undefined || Number.isNaN(ns)) {
      return '0m00.00s';
    }
    const totalMs = ns / 1e6;
    const minute = Math.floor(totalMs / 60000);
    const second = Math.floor((totalMs % 60000) / 1000);
    const ms = Math.round((totalMs % 1000) / 10);
    return `${minute}m${String(second).padStart(2, '0')}.${String(ms).padStart(2, '0')}s`;
  }

  /**
   * Formats an interval in nanoseconds into a human-friendly duration.
   * @param {number} ns
   * @return {string}
   */
  formatIntervalMetricHuman(ns) {
    if (ns === null || ns === undefined || Number.isNaN(ns)) {
      return '0.00s';
    }
    const ms = ns / 1e6;
    if (ms > 10) {
      const totalSec = ms / 1000;
      const mins = Math.floor(totalSec / 60);
      const secs = totalSec % 60;
      if (mins > 0) {
        return `${mins}m${secs < 10 ? '0' : ''}${secs.toFixed(2)}s`;
      }
      return `${secs.toFixed(2)}s`;
    } else {
      const us = (ns % 1e6) / 1000;
      return `${Math.floor(ms)}.${String(Math.round(us / 10)).padStart(2, '0')}ms`;
    }
  }

  /**
   * Fetches step metrics data from endpoint.
   */
  async fetchData() {
    if (!this.endpoint) {
      return;
    }
    this._loading = true;
    this._error = '';
    try {
      const response = await fetch(this.endpoint);
      if (!response.ok) {
        throw new Error(`HTTP error! status: ${response.status}`);
      }
      this._data = await response.json();
    } catch (e) {
      this._error = e.message;
    } finally {
      this._loading = false;
    }
  }

  get filteredSteps() {
    if (!this._data || !this._data.steps) {
      return [];
    }
    return filterSteps(this._data.steps, this.queryState);
  }

  get sortedSteps() {
    return sortSteps(this.filteredSteps, this.queryState);
  }

  /**
   * Renders the execution timeline SVG for a build step.
   * @param {!object} step
   * @return {!import('lit').TemplateResult}
   */
  renderTimeline(step) {
    const buildDuration = this._data?.buildDurationNanos || 1;
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
          : ''}
      </svg>
    `;
  }

  updated(changedProperties) {
    super.updated(changedProperties);
    if (changedProperties.has('sidebar') || !this._sidebarBinder.isBound) {
      this._bindSidebar();
    }
    const sorted = this.sortedSteps;
    const pagination = this.queryState.getPagination(sorted.length);
    this._sidebarBinder.sync(pagination);
  }

  render() {
    if (this._loading) {
      return html`
        <div class="surface" style="padding: 2rem; text-align: center;">
          <md-linear-progress indeterminate style="width: 100%; max-width: 360px; margin: 0 auto 16px;"></md-linear-progress>
          <p style="color: var(--md-sys-color-on-surface-variant); margin: 0;">Loading build steps...</p>
        </div>
      `;
    }

    if (this._error) {
      return html`
        <div class="surface build-status failure">
          <md-icon>cancel</md-icon>
          <span>Failed to load build steps: ${this._error}</span>
        </div>
      `;
    }

    const sorted = this.sortedSteps;
    const pagination = this.queryState.getPagination(sorted.length);
    const currentSubset = sorted.slice(pagination.itemsFirst, pagination.itemsLast);

    const isReadyAsc = this.queryState.sortBy === 'ready' && !this.queryState.sortDsc;
    const isReadyDsc = this.queryState.sortBy === 'ready' && this.queryState.sortDsc;
    const isDurationAsc = this.queryState.sortBy === 'duration' && !this.queryState.sortDsc;
    const isDurationDsc = this.queryState.sortBy === 'duration' && this.queryState.sortDsc;
    const isCompletionAsc = this.queryState.sortBy === 'completion' && !this.queryState.sortDsc;
    const isCompletionDsc = this.queryState.sortBy === 'completion' && this.queryState.sortDsc;
    const isCriticalPath = this.queryState.view === 'criticalPath';

    const outdirRel = this._data?.outdirRel || '';

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
              ${!isCriticalPath
                ? html`
                    <th class="timestamp-column" scope="col">
                      <md-text-button
                        class="sort-header"
                        trailing-icon
                        @click=${() => this.queryState.toggleSort('ready')}
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
                        @click=${() => this.queryState.toggleSort('duration')}
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
                        @click=${() => this.queryState.toggleSort('completion')}
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
            ${currentSubset.map((step) => {
              const output = step.outputs?.[0] || "";
              const trimmedOutput =
                outdirRel && output.startsWith(outdirRel + '/')
                  ? output.slice(outdirRel.length + 1)
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

// TODO: https://google.github.io/styleguide/jsguide.html#features-strings-use-single-quotes
customElements.define("siso-step-table", SisoStepTable);
