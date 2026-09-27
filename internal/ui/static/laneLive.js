// laneLive.js drives the Sessions page's live updates: every 5 seconds while
// the tab is visible, it asks the server which sessions are still active
// (still holding a live affinity pin — the same "hot dot" definition the
// page itself renders) and swaps in a freshly rendered copy of each one's
// lane, plus the nav bar's "N active" Sessions stat.
//
// It is hand-written rather than `hx-trigger="every 5s"` for the same
// reasons live.js (the old request list's tail) is: it must not poll a tab
// nobody is looking at, it must not treat one query failure as a
// page-breaking error, and it must say when it has given up rather than
// polling silently into the void.
//
// Unlike live.js, this is not a cursor-based *tail* — there is no "new rows
// since X" here, because a lane is not an append-only log: a session's own
// header (turn count, cost, preview) and its whole timeline can all change
// between polls as the conversation continues. So each poll asks "what do
// the currently-active lanes look like right now" and swaps each one
// wholesale, keyed by session id, rather than appending anything.
(function () {
  "use strict";

  // Matches live.js's own interval — "as long as we are not impacting the
  // routing capacity" was the brief, and 5s is the number already accepted
  // for the request list's tail on this same single-user deployment.
  var POLL_MS = 5000;

  var MAX_FAILURES = 3;

  // The currently running poller's teardown, if any — see init()'s
  // htmx:afterSwap handling for why this needs to be torn down and rebuilt
  // rather than assumed to survive a filter change.
  var activeStop = null;

  function setup(root) {
    var src = root.getAttribute("data-live-src");
    if (!src) {
      return;
    }

    var timer = null;
    var inFlight = false;
    var failures = 0;

    function poll() {
      if (inFlight) {
        return;
      }
      inFlight = true;

      var qs = window.location.search; // echo the page's own filters verbatim
      var url = src + (qs ? qs : "");

      fetch(url, { headers: { Accept: "application/json" }, cache: "no-store" })
        .then(function (res) {
          if (!res.ok) {
            throw new Error("HTTP " + res.status);
          }
          return res.json();
        })
        .then(function (payload) {
          failures = 0;
          inFlight = false;

          if (payload.error) {
            // A poll failing is a condition to retry, not to surface loudly —
            // the page already rendered fine on load; this is a courtesy
            // refresh on top of it. Consecutive failures still count below.
            return;
          }

          applyLanes(payload.lanes || []);
          applyActiveCount(payload.active_count);
        })
        .catch(function () {
          inFlight = false;
          failures++;
          if (failures >= MAX_FAILURES && timer) {
            window.clearInterval(timer);
            timer = null;
          }
        });
    }

    // stableHTML is a lane's markup with the state that is NOT
    // server-rendered stripped out — exactly what the page mutates in place
    // on a lane it has already rendered, plus the one render that is not a
    // data change:
    //
    //   * laneTimeline.js's per-node `margin-right` (the fit that compresses
    //     a dense lane's spacing so its whole history fits without scrolling)
    //   * laneDetail.js's `is-selected` / `selected` selection classes
    //   * the header's relative "started 12m ago" text (.ago), which is a
    //     function of the wall clock, not of the session: it re-renders
    //     differently on every poll for any session younger than an hour
    //     ("0s ago", "1m ago", …) with nothing having happened. Left in, it
    //     would make every young lane read as changed on every single poll —
    //     defeating this comparison exactly for the sessions live-updating
    //     exists for, and one hour is precisely the window in which a dense
    //     lane is most likely to be growing.
    //
    // Both sides are compared through the SAME serializer: the incoming
    // payload is parsed into an element first, so Go's `&#34;` escaping and
    // the browser's `&quot;` re-serialization of the same attribute value
    // cannot make two identical lanes read as different.
    function stableHTML(el) {
      var clone = el.cloneNode(true);
      var all = clone.querySelectorAll("*");
      for (var i = 0; i < all.length; i++) {
        if (all[i].getAttribute("style") !== null) {
          all[i].removeAttribute("style");
        }
        all[i].classList.remove("is-selected");
        if (all[i].classList.contains("ago")) {
          all[i].textContent = "";
        }
      }
      clone.classList.remove("selected");
      return clone.outerHTML;
    }

    // applyLanes swaps each returned lane's markup in place, keyed by its
    // session id (the .lane-row's data-session attribute — see laneRow.html).
    // A lane not currently on screen (filtered out client-side is not a
    // thing here, but the page may have navigated to a different query
    // between the fetch firing and it landing) is skipped rather than
    // inserted: this poller only ever refreshes lanes the page already
    // decided to show, never adds new ones — a session going live for the
    // first time appears on the next full load or filter change, same as
    // the "live only" toggle already implies for anything outside its
    // grace window.
    //
    // A lane whose fresh render is IDENTICAL to what is already on screen is
    // left completely alone rather than replaced with a copy. That is not an
    // optimization: `.lane-timeline { overflow: hidden }`, so a replaced
    // lane arrives at its natural, uncompressed width and gets clipped —
    // the fit laneTimeline.js had already applied to the copy on screen is
    // discarded with the node it was applied to. Re-rendering a lane that
    // did not change therefore *loses* information (the spacing fit and any
    // node selection) while gaining nothing. Most polled lanes are in
    // exactly this state: the poll refreshes every *pinned* session, and a
    // pinned session is very often idle.
    function applyLanes(lanes) {
      var swapped = false;
      for (var i = 0; i < lanes.length; i++) {
        var lane = lanes[i];
        if (!lane.html) {
          continue;
        }
        var existing = document.querySelector(
          '.lane-row[data-session="' + cssEscape(lane.key) + '"]'
        );
        if (!existing) {
          continue;
        }
        var replacement = fromHTML(lane.html);
        if (!replacement) {
          continue;
        }
        if (stableHTML(replacement) === stableHTML(existing)) {
          continue;
        }
        if (window.ArbiterLaneDetail) {
          window.ArbiterLaneDetail.clearSelectionIfWithin(existing);
        }
        existing.replaceWith(replacement);
        swapped = true;
      }
      // A lane that really changed has just been replaced with markup that
      // carries no fit, and this poll fires no htmx event, so re-run the fit
      // once at the end rather than per lane (scheduleFit coalesces anyway, but
      // one call is the honest shape). Safe if laneTimeline.js has not run
      // yet: it is loaded after this file, and this is only reached on a poll,
      // long after both have executed.
      if (swapped && window.ArbiterLaneTimeline) {
        window.ArbiterLaneTimeline.fitAll();
      }
    }

    function applyActiveCount(n) {
      if (typeof n !== "number") {
        return;
      }
      var stat = document.querySelector('[data-nav-stat="sessions"]');
      if (!stat) {
        return;
      }
      var unit = stat.querySelector(".u");
      stat.textContent = String(n);
      if (unit) {
        stat.appendChild(unit);
      }
    }

    function fromHTML(html) {
      var holder = document.createElement("div");
      holder.innerHTML = html;
      return holder.firstElementChild;
    }

    // cssEscape guards a session key against being read as CSS selector
    // syntax (a content-derived key is 64 hex chars and always safe, but a
    // header-supplied one is arbitrary text — see sessionKeyDisplayLen's own
    // doc comment on why a session key is never assumed to be "just" hex).
    function cssEscape(s) {
      if (window.CSS && window.CSS.escape) {
        return window.CSS.escape(s);
      }
      return String(s).replace(/[^a-zA-Z0-9_-]/g, "\\$&");
    }

    function start() {
      poll();
      timer = window.setInterval(poll, POLL_MS);
    }

    function stop() {
      if (timer) {
        window.clearInterval(timer);
        timer = null;
      }
    }

    // Pause while the tab is hidden, resume on return — a hidden tab has no
    // reader to show a live view to, and hammering the store for one is
    // exactly what a single-user deployment's routing capacity should not
    // spend on.
    document.addEventListener("visibilitychange", function () {
      if (document.hidden) {
        stop();
      } else if (!timer && failures < MAX_FAILURES) {
        start();
      }
    });

    start();
    activeStop = stop;
  }

  function init() {
    var root = document.getElementById("lane-list");
    if (root) {
      setup(root);
    }
  }

  // The toolbar's filter form swaps #lane-list wholesale (hx-target="#lane-list"
  // hx-swap="outerHTML" — see laneRow.html), which replaces the very element
  // this poller's interval closure was reading data-live-src and querying
  // .lane-row elements from. Left alone the old poller would keep running
  // against detached nodes (harmless but useless) while the new list gets no
  // poller at all — the same class of bug live.js's own re-init on swap
  // avoids for the request list's tail. So: tear down the old poller and set
  // up a fresh one on the new #lane-list every time htmx replaces it.
  document.body.addEventListener("htmx:afterSwap", function (ev) {
    if (ev.target && ev.target.id === "lane-list") {
      if (activeStop) {
        activeStop();
        activeStop = null;
      }
      setup(ev.target);
    }
  });

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
