// laneTimeline.js — compresses each lane's timeline SPACING to fit the
// lane's own width, so a session with many requests never scrolls the
// timeline horizontally (the lane list only ever scrolls vertically — see
// #lane-list/.lane-scroll in app.css). A lane is exactly as wide as
// .lanes-col; every node in it has to fit inside that, however many
// requests the session had.
//
// Unlike the first version of this file, node/pill SIZES never change —
// only the GAP between them does. Shrinking a stream pill's box along with
// everything else made its count label illegible exactly when it mattered
// most (the densest lanes); a pill's text must stay the same size at any
// density. So this sets an explicit `margin-right` (px) on each node
// directly instead of a shared `--node-scale` custom property.
//
// Two stages, in order:
//   1. Shrink every node's gap uniformly (by the same fraction of its own
//      natural-to-floor range) until the lane fits or every gap has hit
//      its floor — a satellite, a stream pill, and a client node all keep
//      a minimum breathing gap, they just get closer together.
//   2. If the lane still doesn't fit once every gap is at its floor, start
//      pulling immediate neighbors on top of plain/"ok"-status client
//      nodes only (negative margin = overlap) — never a satellite, a
//      stream pill, or an errored/warned/noted client node. Capped so an
//      overlapped node is never fully hidden. This is the "only overlap
//      the green/normal ones, keep the rest the same" behavior.
// A lane with few nodes never gets any of this — it stays at its natural
// spacing, matching the mockup's normal case.
(function () {
  "use strict";

  // Must match the fixed margin-right values in app.css's
  // .node.client/.sat/.stack rules (the "natural", scale-1 spacing).
  var NATURAL_MARGIN = { client: 38, sat: 26, stack: 34 };
  // Stage 1 never shrinks a gap below this — small enough to pack a lot
  // in, large enough that adjacent nodes are still visibly separate.
  var FLOOR_MARGIN = { client: 6, sat: 4, stack: 10 };
  // Stage 2 cap: how far a plain-ok client node's margin can go negative
  // (i.e., how much of its neighbor's leading edge can sit on top of it).
  // Bounded well under the node's own 18px width so it's overlapped, never
  // fully swallowed — there's always a sliver left to click or see.
  var MAX_OVERLAP = 12;

  function nodeType(node) {
    if (node.classList.contains("sat")) return "sat";
    if (node.classList.contains("stack")) return "stack";
    return "client";
  }

  // A "plain/green/normal" node: a client request that came back ok. Any
  // other status (err/warn/note) or a satellite/stream-pill is exempt from
  // stage 2 — it keeps its floor gap no matter how dense the lane gets.
  function isPlainOk(node) {
    return node.classList.contains("client") && node.classList.contains("s-ok");
  }

  function fit(row) {
    var timeline = row.querySelector(".lane-timeline");
    if (!timeline) return;
    var nodes = timeline.querySelectorAll(".node");
    if (!nodes.length) return;

    // Reset to natural spacing before measuring — otherwise a previous
    // fit's compression would make the "does it overflow" check measure
    // the already-compressed width and never re-expand after the lane
    // gets wider.
    for (var i = 0; i < nodes.length; i++) {
      nodes[i].style.marginRight = "";
    }

    var available = timeline.clientWidth;
    var natural = timeline.scrollWidth;
    if (available <= 0 || natural <= available) return;

    var overflow = natural - available;

    // Stage 1: how much combined slack does shrinking every gap to its
    // floor give us? Take exactly the fraction of that needed to close
    // `overflow` — shared uniformly, so every node loses the same
    // proportion of its own natural-to-floor range.
    var slack = 0;
    for (var j = 0; j < nodes.length; j++) {
      var t0 = nodeType(nodes[j]);
      slack += NATURAL_MARGIN[t0] - FLOOR_MARGIN[t0];
    }
    var stage1Ratio = slack > 0 ? Math.min(overflow / slack, 1) : 1;
    for (var k = 0; k < nodes.length; k++) {
      var t1 = nodeType(nodes[k]);
      var range = NATURAL_MARGIN[t1] - FLOOR_MARGIN[t1];
      nodes[k].style.marginRight = (NATURAL_MARGIN[t1] - range * stage1Ratio) + "px";
    }

    if (stage1Ratio < 1) return; // stage 1 alone closed the gap

    var remaining = overflow - slack;
    if (remaining <= 0) return;

    // Stage 2: every gap is already at its floor and the lane still
    // overflows. Only plain-ok client nodes may give up more space now,
    // by overlapping the node that follows them.
    var eligible = [];
    for (var m = 0; m < nodes.length; m++) {
      if (isPlainOk(nodes[m])) eligible.push(nodes[m]);
    }
    if (!eligible.length) return; // nothing left that's allowed to shrink further

    var overlapEach = Math.min(remaining / eligible.length, MAX_OVERLAP);
    for (var n = 0; n < eligible.length; n++) {
      var t2 = nodeType(eligible[n]); // always "client" here, kept for clarity
      eligible[n].style.marginRight = (FLOOR_MARGIN[t2] - overlapEach) + "px";
    }
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
