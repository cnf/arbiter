// laneDetail.js — drives the Sessions page's persistent detail panel
// (#52's replacement for the old click-opens-a-modal interaction): clicking
// any timeline node renders that node's already-server-rendered data-*
// facts into #detailCol, entirely client-side. No fetch, no second render
// path for the node's own fields — the timeline already carries everything
// the panel needs, just as data attributes instead of visible text (see
// laneRow.html's lane-node/lane-satellite templates).
//
// Delegated to document, same pattern as popover.js/showmore.js, so it
// keeps working after the toolbar's htmx swap replaces #lane-list wholesale.
(function () {
  "use strict";

  var selected = null;

  function esc(s) {
    var d = document.createElement("div");
    d.textContent = s == null ? "" : String(s);
    return d.innerHTML;
  }

  function render(node) {
    var d = node.dataset;
    var isStack = node.classList.contains("stack");
    var kindLabel = d.kind === "classifier" ? "classifier" : (d.kind || "client");
    var rows = [
      ["route", d.route || "—"],
      ["provider", d.provider || "—"],
      ["kind", kindLabel],
      ["status", d.status || "—"],
      ["cost", d.cost || "—"],
      ["latency", d.latency || "—"],
    ];
    if (isStack) {
      rows.push(["streamed turns", d.count]);
    }
    if (d.trace) {
      rows.push(["trace", d.trace]);
    }
    var statGrid = rows
      .map(function (r) {
        return (
          '<div class="stat"><dt>' + esc(r[0]) + "</dt><dd>" + esc(r[1]) + "</dd></div>"
        );
      })
      .join("");
    var openLink = "";
    if (d.openHref) {
      openLink = '<a class="open-link" href="' + esc(d.openHref) + '">open flat list →</a>';
    } else if (d.requestId) {
      openLink =
        '<a class="open-link" href="/admin/ui/requests?highlight=' +
        encodeURIComponent(d.requestId) +
        '">open in requests →</a>';
    }
    document.getElementById("detailCol").innerHTML =
      '<div class="node-panel">' +
      "<h2>" +
      (isStack ? esc(d.count) + "× streamed turns" : "Request") +
      "</h2>" +
      '<div class="stat-grid">' +
      statGrid +
      "</div>" +
      (d.rationale ? '<div class="hint">' + esc(d.rationale) + "</div>" : "") +
      openLink +
      "</div>";
  }

  function clearSelection() {
    if (selected) {
      selected.classList.remove("is-selected");
      var row = selected.closest(".lane-row");
      if (row) {
        row.classList.remove("selected");
      }
      selected = null;
    }
    var def = document.getElementById("lane-detail-default-src");
    if (def) {
      document.getElementById("detailCol").innerHTML = def.innerHTML;
    }
  }

  function select(node) {
    if (selected === node) {
      clearSelection();
      return;
    }
    if (selected) {
      selected.classList.remove("is-selected");
      var prevRow = selected.closest(".lane-row");
      if (prevRow) {
        prevRow.classList.remove("selected");
      }
    }
    selected = node;
    node.classList.add("is-selected");
    var row = node.closest(".lane-row");
    if (row) {
      row.classList.add("selected");
    }
    render(node);
  }

  document.addEventListener("click", function (ev) {
    var node = ev.target.closest(".node");
    if (node) {
      select(node);
      return;
    }
    // Clicking the lane background (not a node, not the panel itself)
    // restores the default aggregate view.
    if (!ev.target.closest("#detailCol") && !ev.target.closest(".toolbar")) {
      clearSelection();
    }
  });

  document.addEventListener("keydown", function (ev) {
    if (ev.key === "Enter" || ev.key === " ") {
      var node = ev.target.closest(".node");
      if (node) {
        ev.preventDefault();
        select(node);
      }
    } else if (ev.key === "Escape") {
      clearSelection();
    }
  });
})();
