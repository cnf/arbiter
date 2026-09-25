// laneTimeline.js — compresses each lane's timeline nodes to fit the lane's
// own width, so a session with many requests never scrolls the timeline
// horizontally (the lane list only ever scrolls vertically — see
// #lane-list/.lane-scroll in app.css). A lane is exactly as wide as
// .lanes-col; every node in it has to fit inside that, however many
// requests the session had.
//
// The approach: every node's CSS size and right-margin are `calc(Npx *
// var(--node-scale, 1))` (see app.css's .node.client/.sat/.stack rules).
// This script measures a lane's timeline at scale 1 (its natural,
// uncompressed width) against the track's available width and, if the
// natural width would overflow, sets --node-scale on that one
// .lane-timeline to the ratio that makes it fit exactly. A lane with few
// nodes never gets a scale set at all — it stays at its natural size,
// matching the mockup's normal case.
//
// Same lifecycle problem chart.js documents: htmx replaces #lane-list
// wholesale on every filter change, so this re-measures on every
// htmx:afterSettle (post-layout, so clientWidth is meaningful) in addition
// to first load, and a ResizeObserver keeps it correct across window/panel
// resizes without a resize listener.
(function () {
  "use strict";

  function fit(row) {
    var timeline = row.querySelector(".lane-timeline");
    if (!timeline) return;

    // Reset to natural size before measuring — otherwise a previous fit's
    // shrink would make the "does it overflow" check measure the already-
    // compressed width and never re-expand after the lane gets wider.
    timeline.style.removeProperty("--node-scale");

    // scrollWidth is the content's natural width regardless of the
    // overflow:hidden clip on .lane-timeline (app.css) — exactly the
    // "how wide would this be uncompressed" figure the fit needs.
    var natural = timeline.scrollWidth;
    var available = timeline.clientWidth;
    if (available <= 0 || natural <= available) return;

    // A small floor keeps very dense lanes (hundreds of nodes) from
    // shrinking into illegible or zero-size dots; past this point the
    // stack/run-collapsing the lane already does (see sessions.go's
    // foldRequestLines) is the real answer, not further compression.
    var scale = Math.max(available / natural, 0.28);
    timeline.style.setProperty("--node-scale", String(scale));
  }

  function fitAll() {
    document.querySelectorAll(".lane-row").forEach(fit);
  }

  var pending = false;
  function scheduleFit() {
    if (pending) return;
    pending = true;
    requestAnimationFrame(function () {
      pending = false;
      fitAll();
    });
  }

  // afterSettle, not afterSwap: nodes must be laid out (post-settle) before
  // scrollWidth/clientWidth are meaningful, same reasoning as chart.js.
  document.addEventListener("htmx:afterSettle", scheduleFit);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", scheduleFit);
  } else {
    scheduleFit();
  }

  if (typeof ResizeObserver !== "undefined") {
    var ro = new ResizeObserver(scheduleFit);
    var lanesCol = document.querySelector(".lanes-col");
    if (lanesCol) ro.observe(lanesCol);
  }
})();
