// popover.js — the full-screen popover component (DESIGN.md's "Full-screen
// popover"). One component, wired up by three later call sites: the session
// transcript (#48), the sessions index first-message preview (#49), and
// discovery block previews (#50). Built here, in the foundation phase, so
// none of them re-implements it.
//
// Contract: a trigger element that opens content into the popover carries
// data-popover-trigger, plus either
//   - data-popover-text: the content to show verbatim (already-escaped HTML
//     is never accepted here — see below), or
//   - data-popover-src: a URL to htmx-fetch into the popover body.
// data-popover-title (optional) sets the header title; data-popover-meta
// (optional) sets the small mono text on the header's right side (e.g. a
// char count).
//
// Content is always inserted as text (textContent), never as innerHTML, for
// a static data-popover-text trigger — the same "no stored body is ever
// marked safe" rule the server-rendered templates follow. A data-popover-src
// fetch is swapped in by htmx itself using the same escaping/markup its own
// fragment response carries, exactly as every other htmx swap on this page
// does; this script does not touch that path beyond pointing hx-target at
// the popover body.
(function () {
  "use strict";

  var overlay = null;
  var titleEl = null;
  var metaEl = null;
  var bodyEl = null;
  var closeBtn = null;
  var lastFocus = null;

  function build() {
    overlay = document.createElement("div");
    overlay.className = "popover-overlay";
    overlay.hidden = true;
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");

    var panel = document.createElement("div");
    panel.className = "popover";

    var header = document.createElement("div");
    header.className = "popover-header";

    titleEl = document.createElement("strong");
    titleEl.className = "popover-title";

    metaEl = document.createElement("span");
    metaEl.className = "popover-meta";

    closeBtn = document.createElement("button");
    closeBtn.type = "button";
    closeBtn.className = "popover-close";
    closeBtn.textContent = "close";
    closeBtn.addEventListener("click", close);

    header.appendChild(titleEl);
    header.appendChild(metaEl);
    header.appendChild(closeBtn);

    bodyEl = document.createElement("div");
    bodyEl.className = "popover-body";
    bodyEl.id = "popover-body";

    panel.appendChild(header);
    panel.appendChild(bodyEl);
    overlay.appendChild(panel);
    document.body.appendChild(overlay);

    overlay.addEventListener("click", function (ev) {
      if (ev.target === overlay) {
        close();
      }
    });
    document.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape" && !overlay.hidden) {
        close();
      }
    });

    // htmx only scans the document for its own attributes once at startup and
    // again after a swap it performed; this overlay is created after that
    // initial scan, so a freshly built data-popover-src child needs an
    // explicit process() call. See openSrc below.
  }

  function open(title, meta) {
    if (!overlay) {
      build();
    }
    titleEl.textContent = title || "";
    metaEl.textContent = meta || "";
    overlay.hidden = false;
    // Lock background scroll while the popover is open — a popover that lets
    // the page scroll behind it is a bug, not a variant (DESIGN.md).
    document.documentElement.style.overflow = "hidden";
    lastFocus = document.activeElement;
    closeBtn.focus();
  }

  function close() {
    if (!overlay || overlay.hidden) {
      return;
    }
    overlay.hidden = true;
    bodyEl.textContent = "";
    document.documentElement.style.overflow = "";
    if (lastFocus && typeof lastFocus.focus === "function") {
      lastFocus.focus();
    }
  }

  function openText(title, meta, text) {
    open(title, meta);
    // textContent, never innerHTML: a data-popover-text value is untrusted
    // stored content and must go in as a plain string, exactly like every
    // <pre> the server renders.
    bodyEl.textContent = text || "";
  }

  function openSrc(title, meta, src) {
    open(title, meta);
    bodyEl.textContent = "";
    bodyEl.setAttribute("hx-get", src);
    bodyEl.setAttribute("hx-trigger", "popover-open");
    bodyEl.setAttribute("hx-swap", "innerHTML");
    if (window.htmx) {
      window.htmx.process(bodyEl);
      window.htmx.trigger(bodyEl, "popover-open");
    }
  }

  function onClick(ev) {
    var trigger = ev.target.closest("[data-popover-trigger]");
    if (!trigger) {
      return;
    }
    ev.preventDefault();
    var title = trigger.getAttribute("data-popover-title") || "";
    var meta = trigger.getAttribute("data-popover-meta") || "";
    var src = trigger.getAttribute("data-popover-src");
    if (src) {
      openSrc(title, meta, src);
      return;
    }
    openText(title, meta, trigger.getAttribute("data-popover-text") || "");
  }

  function init() {
    document.addEventListener("click", onClick);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
