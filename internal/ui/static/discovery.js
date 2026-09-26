// discovery.js — drives the Discovery page's detail pane and workspace mode
// (#50). Same shape as laneDetail.js: every row's facts already arrived as
// data-* attributes on server render (see discoveryRow.html's discovery-row
// template), so selecting a row is a pure client-side re-render with no
// second fetch. What this page fetches on demand: a block's full stored body
// on entering workspace mode (the row only ever carries a 200-char preview,
// see renderWorkspace), a block's variant list, and a per-request diff
// fragment — all routed through htmx (hx-get/hx-post on the elements
// discovery.js itself creates), not through this file's own XHR calls.
//
// Interaction model, from the mockup's footer hint: click a row to select
// and show it in the pane; click the dot to cycle unseen -> seen -> ignored
// (handled by the button's own hx-post, see discoveryRow.html); Up/Down
// moves the selection; Enter or double-click opens workspace mode (the pane
// takes over most of .main, the ledger narrows to a label-only rail);
// Escape leaves workspace mode. Selecting never navigates — only
// "show all sessions" (a plain link in the pane) does.
(function () {
  "use strict";

  var selected = null; // the selected <tr>, or null
  var workspace = false;

  function esc(s) {
    var d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function rowsContainer() {
    return document.getElementById("discoveryBody");
  }

  function main() {
    return document.querySelector(".main");
  }

  function tableWrap() {
    return document.getElementById("discoveryTableWrap");
  }

  function detailPane() {
    return document.getElementById("discoveryDetail");
  }

  // defaultPaneHTML is rebuilt fresh rather than cached at load: the page's
  // htmx toolbar can re-fetch #repeated-list (a filter change), which does
  // not touch #discoveryDetail, so nothing here needs to survive that swap —
  // but rebuilding on every clear keeps this file from depending on markup
  // that lived in a fragment which may since have been replaced.
  function defaultPaneHTML() {
    return (
      '<div class="default-panel"><h2>Discovery</h2><div class="hint">' +
      "Click a pattern to inspect its sample text here. Click the dot to " +
      "cycle unseen \u2192 seen \u2192 ignored. Double-click, or press Enter on a " +
      "selected row, to open it full-screen. Escape returns to the list. " +
      "Seen patterns re-flag as unseen if they reappear after the mark; " +
      "ignored ones don't.</div></div>"
    );
  }

  function clearSelection() {
    if (selected) {
      selected.classList.remove("selected");
      selected = null;
    }
    detailPane().classList.remove("workspace");
    detailPane().innerHTML = defaultPaneHTML();
  }

  function renderDetail() {
    if (!selected) {
      return;
    }
    var d = selected.dataset;
    if (workspace) {
      renderWorkspace(d);
    } else {
      renderInline(d);
    }
  }

  function metaRow(d) {
    return (
      '<div class="meta-row">' +
      "<span><b>" + esc(d.sessions) + "</b> sessions</span>" +
      "<span>first <b>" + esc(d.firstSeen) + "</b></span>" +
      "<span>last <b>" + esc(d.lastSeen) + "</b></span>" +
      "</div>"
    );
  }

  function renderInline(d) {
    var pane = detailPane();
    pane.classList.remove("workspace");
    pane.innerHTML =
      '<div class="ws-toolbar"><h2 style="margin:0">' +
      (d.role ? esc(d.role) : "(no role)") +
      " \u00b7 " +
      esc(d.blockType) +
      '</h2><div class="spacer"></div>' +
      '<button type="button" class="ws-back" id="wsOpen">open workspace \u2192</button></div>' +
      metaRow(d) +
      '<div class="section-label">sample content (first 200 chars \u2014 open workspace for the rest)</div>' +
      '<div class="body-text">' +
      esc(d.preview) +
      "</div>" +
      '<a class="open-link" href="' +
      esc(d.blockUrl) +
      '">show all sessions containing this block \u2192</a>';
    var open = document.getElementById("wsOpen");
    if (open) {
      open.addEventListener("click", enterWorkspace);
    }
  }

  function renderWorkspace(d) {
    var pane = detailPane();
    pane.classList.add("workspace");
    pane.innerHTML =
      '<div class="ws-toolbar"><button type="button" class="ws-back" id="wsBack">\u2190 back to list</button></div>' +
      "<h2>" +
      (d.role ? esc(d.role) : "(no role)") +
      " \u00b7 " +
      esc(d.blockType) +
      "</h2>" +
      metaRow(d) +
      '<div class="ws-scroll">' +
      '<div class="section-label">sample content</div>' +
      '<div class="body-text ws-full" id="wsBody" hx-get="/admin/ui/content/block/body?hash=' +
      encodeURIComponent(d.hash) +
      '" hx-trigger="wsload" hx-swap="innerHTML">loading\u2026</div>' +
      '<a class="open-link" href="' +
      esc(d.blockUrl) +
      '">show all sessions containing this block \u2192</a>' +
      "</div>";
    var back = document.getElementById("wsBack");
    if (back) {
      back.addEventListener("click", exitWorkspace);
    }
    // The full body is fetched on entry rather than embedded in the row's
    // own data-* attributes: RepeatedContent only ever carries a 200-char
    // SQL preview (kept small so the cross-session GROUP BY never drags
    // megabyte bodies through the aggregation), so workspace mode — the one
    // place that preview isn't enough — asks for the real thing the same
    // way the block drill-down page already does. Same htmx.process/trigger
    // pattern popover.js's openSrc uses for its own on-demand fetch.
    var body = document.getElementById("wsBody");
    if (body && window.htmx) {
      window.htmx.process(body);
      window.htmx.trigger(body, "wsload");
    }
  }

  function select(tr) {
    if (selected) {
      selected.classList.remove("selected");
    }
    selected = tr;
    tr.classList.add("selected");
    renderDetail();
  }

  function enterWorkspace() {
    workspace = true;
    var m = main();
    if (m) {
      m.classList.add("workspace-mode");
    }
    var tw = tableWrap();
    if (tw) {
      tw.classList.add("rail");
    }
    renderDetail();
  }

  function exitWorkspace() {
    workspace = false;
    var m = main();
    if (m) {
      m.classList.remove("workspace-mode");
    }
    var tw = tableWrap();
    if (tw) {
      tw.classList.remove("rail");
    }
    renderDetail();
  }

  document.addEventListener("click", function (ev) {
    var tr = ev.target.closest(".repeated-row");
    if (tr) {
      if (ev.target.closest(".state-dot")) {
        return; // the dot's own hx-post handles the click
      }
      select(tr);
      return;
    }
    // Clicking the ledger background (not a row, not the pane, not the
    // toolbar) clears the selection — same convention laneDetail.js uses.
    if (
      !ev.target.closest("#discoveryDetail") &&
      !ev.target.closest(".toolbar") &&
      !workspace
    ) {
      clearSelection();
    }
  });

  document.addEventListener("dblclick", function (ev) {
    var tr = ev.target.closest(".repeated-row");
    if (!tr || ev.target.closest(".state-dot")) {
      return;
    }
    select(tr);
    enterWorkspace();
  });

  document.addEventListener("keydown", function (ev) {
    if (ev.target.tagName === "INPUT" || ev.target.tagName === "SELECT") {
      return;
    }
    if (ev.key === "ArrowDown" || ev.key === "ArrowUp") {
      var body = rowsContainer();
      if (!body) {
        return;
      }
      var rows = Array.prototype.slice.call(
        body.querySelectorAll(".repeated-row")
      );
      if (!rows.length) {
        return;
      }
      ev.preventDefault();
      var idx = selected ? rows.indexOf(selected) : -1;
      var delta = ev.key === "ArrowDown" ? 1 : -1;
      idx = idx === -1 ? 0 : Math.min(rows.length - 1, Math.max(0, idx + delta));
      select(rows[idx]);
      rows[idx].scrollIntoView({ block: "nearest" });
      return;
    }
    if (ev.key === "Enter" && !workspace && selected) {
      ev.preventDefault();
      enterWorkspace();
      return;
    }
    if (ev.key === "Escape" && workspace) {
      ev.preventDefault();
      exitWorkspace();
    }
  });

  // A filter/threshold change re-fetches #repeated-list wholesale (see the
  // toolbar's hx-target in discoveryRow.html), which drops whatever row was
  // selected along with the old markup — clear the pane back to its default
  // rather than pointing at a detached element.
  document.body.addEventListener("htmx:afterSwap", function (ev) {
    if (ev.detail && ev.detail.target && ev.detail.target.id === "repeated-list") {
      selected = null;
      if (!workspace) {
        detailPane().innerHTML = defaultPaneHTML();
      }
    }
  });
})();
