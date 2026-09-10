// Copyright 2026 The Chromium Authors
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

/**
 * Binds the server-rendered #page-sidebar form controls to a StepQueryState model,
 * and synchronizes pagination information back to the sidebar DOM.
 *
 * This is a TEMPORARY stand-in to progressively migrate the step renderer from a
 * purely server-side rendered architecture to use "islands" of web components.
 *
 * A followup MUST strongly consider whether the sidebar filter controls can be
 * rewritten as a web component.
 */
export class StepSidebarBinder {
  /**
   * @param {import('./step-query-state.js').StepQueryState} queryState
   * @param {() => number} getTotalItems Callback to retrieve current total items count.
   */
  constructor(queryState, getTotalItems) {
    this.queryState = queryState;
    this.getTotalItems = getTotalItems;
    this.sidebarElement = null;
    /** @type {AbortController|null} */
    this._abortController = null;
  }

  /**
   * Returns true if currently bound to a sidebar element.
   * @return {boolean}
   */
  get isBound() {
    return Boolean(this.sidebarElement && this._abortController);
  }

  /**
   * Unbinds all listeners and detaches from the sidebar element.
   */
  unbind() {
    if (this._abortController) {
      this._abortController.abort();
      this._abortController = null;
    }
    this.sidebarElement = null;
  }

  /**
   * Binds user interactions on sidebar controls to the query state.
   * @param {HTMLElement|null} sidebar The sidebar element to bind.
   */
  bind(sidebar) {
    if (!sidebar) {
      return;
    }
    if (this.sidebarElement === sidebar && this._abortController) {
      return;
    }
    if (this.isBound) {
      this.unbind();
    }

    this.sidebarElement = sidebar;
    this._abortController = new AbortController();
    const { signal } = this._abortController;

    // 1. Search text field
    const searchField = sidebar.querySelector('md-filled-text-field[name="q"]');
    if (searchField) {
      searchField.value = this.queryState.searchQuery;
      searchField.addEventListener('input', (e) => {
        this.queryState.setSearchQuery(e.target.value);
      }, { signal });
      const clearBtn = searchField.querySelector('.clear-text-field');
      if (clearBtn) {
        clearBtn.addEventListener('click', () => {
          this.queryState.setSearchQuery('');
        }, { signal });
      }
    }

    // 2. View radio buttons
    const viewRadios = sidebar.querySelectorAll('input[name="view"]');
    for (const radio of viewRadios) {
      if (radio.value === this.queryState.view) {
        radio.checked = true;
      }
      radio.addEventListener('change', (e) => {
        if (e.target.checked) {
          this.queryState.setView(e.target.value);
        }
      }, { signal });
    }

    // 3. Action checkboxes
    const actionCheckboxes = sidebar.querySelectorAll('md-checkbox[name="action"]');
    for (const cb of actionCheckboxes) {
      if (this.queryState.actionFilters.has(cb.value)) {
        cb.checked = true;
      }
      cb.addEventListener('change', () => {
        this.queryState.toggleActionFilter(cb.value);
      }, { signal });
    }

    // 4. Rule checkboxes
    const ruleCheckboxes = sidebar.querySelectorAll('md-checkbox[name="rule"]');
    for (const cb of ruleCheckboxes) {
      if (this.queryState.ruleFilters.has(cb.value)) {
        cb.checked = true;
      }
      cb.addEventListener('change', () => {
        this.queryState.toggleRuleFilter(cb.value);
      }, { signal });
    }

    // 5. Page number input
    const pageInput = sidebar.querySelector('.page-controls .page-number');
    if (pageInput) {
      pageInput.value = String(this.queryState.page);
      pageInput.addEventListener('change', (e) => {
        const val = Number.parseInt(e.target.value, 10);
        if (!Number.isNaN(val)) {
          this.queryState.setPage(val, this.getTotalItems());
        }
      }, { signal });
    }

    // 6. Page navigation buttons
    const pageButtons = sidebar.querySelectorAll('.page-controls md-icon-button');
    if (pageButtons.length >= 4) {
      pageButtons[0].addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
        this.queryState.setPage(0, this.getTotalItems());
      }, { signal });
      pageButtons[1].addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
        this.queryState.setPage(this.queryState.page - 1, this.getTotalItems());
      }, { signal });
      pageButtons[2].addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
        this.queryState.setPage(this.queryState.page + 1, this.getTotalItems());
      }, { signal });
      pageButtons[3].addEventListener('click', (e) => {
        e.preventDefault();
        e.stopPropagation();
        const last = this.queryState.calcPageLast(this.getTotalItems());
        this.queryState.setPage(last, this.getTotalItems());
      }, { signal });
    }

    // 7. Prevent form submit from reloading the page
    const form = sidebar.querySelector('form.data-table-filter');
    if (form) {
      form.addEventListener('submit', (e) => {
        e.preventDefault();
      }, { signal });
    }
  }

  /**
   * Synchronizes pagination display values to the bound sidebar DOM.
   * @param {{
   *   totalItems: number,
   *   pageIndex: number,
   *   pageLast: number,
   *   itemsFirst: number,
   *   itemsLast: number,
   *   itemFirstLogical: number,
   *   itemLastLogical: number
   * }} pagination
   */
  sync(pagination) {
    const sidebar = this.sidebarElement;
    if (!sidebar) {
      return;
    }

    const { totalItems, pageIndex, pageLast, itemFirstLogical, itemLastLogical } = pagination;

    // Update paginator text
    const paginator = sidebar.querySelector('.step-paginator');
    if (paginator) {
      paginator.textContent = `Showing ${itemFirstLogical}-${itemLastLogical} of ${totalItems}`;
    }

    // Update page number input
    const pageInput = sidebar.querySelector('.page-controls .page-number');
    if (pageInput) {
      pageInput.value = String(pageIndex);
      pageInput.max = String(pageLast);
    }

    // Update page button disabled states
    const pageButtons = sidebar.querySelectorAll('.page-controls md-icon-button');
    if (pageButtons.length >= 4) {
      const isFirstPage = pageIndex === 0;
      const isLastPage = pageIndex >= pageLast;
      pageButtons[0].disabled = isFirstPage;
      pageButtons[1].disabled = isFirstPage;
      pageButtons[2].disabled = isLastPage;
      pageButtons[3].disabled = isLastPage;
    }
  }
}
