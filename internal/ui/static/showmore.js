// showmore.js — the gradient-fade "show more" toggle for long block text
// (DESIGN.md's "No nested scrollboxes": >~5 lines fades out with a gradient
// mask and expands inline on click, never an inner overflow:auto box).
//
// CSS does the clamping (see .showmore/.showmore-fade in app.css); this
// script only decides whether a given block is tall enough to need the
// control at all, and flips the expanded state on click. Delegated to
// document, same pattern as popover.js, so it works for content the
// transcript loads after the initial page render (e.g. nothing here today,
// but block content only ever arrives via full htmx swaps, never appended
// piecemeal, so a delegated listener is the resilient default anyway).
(function () {
  "use strict";

  // Below this height a block already fits within the collapsed clamp, so
  // there is nothing to expand — showing a "show more" button that does
  // nothing on click is worse than showing none.
  var CLAMP_PX = 168; // ~7 lines at the transcript's block line-height

  function measure(root) {
    var boxes = root.querySelectorAll(".showmore:not([data-measured])");
    for (var i = 0; i < boxes.length; i++) {
      var box = boxes[i];
      box.setAttribute("data-measured", "1");
      var btn = box.nextElementSibling;
      if (!btn || !btn.classList.contains("showmore-btn")) {
        continue;
      }
      if (box.scrollHeight > CLAMP_PX + 4) {
        btn.hidden = false;
      } else {
        box.classList.add("is-short");
      }
    }
  }

  function onClick(ev) {
    var btn = ev.target.closest(".showmore-btn");
    if (!btn) {
      return;
    }
    var box = btn.previousElementSibling;
    if (!box || !box.classList.contains("showmore")) {
      return;
    }
    var expanded = box.classList.toggle("is-expanded");
    btn.textContent = expanded ? "show less" : "show more";
  }

  function init() {
    measure(document);
    document.addEventListener("click", onClick);
    // A block whose content arrived via an htmx swap (guardrail diff aside,
    // the transcript itself is server-rendered whole) is re-measured after
    // any swap so its show-more button reflects the swapped-in height.
    document.body.addEventListener("htmx:afterSwap", function (ev) {
      measure(ev.target);
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
