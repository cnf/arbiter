// live.js drives the request list's live tail.
//
// It is hand-written rather than `hx-trigger="every 5s"` on the fragment, because
// four things a tail needs are beyond what a declarative trigger can express:
//
//   - it must not poll a tab nobody is looking at (a hidden tab hammering the
//     store for a view no one is reading);
//   - it must keep the rows already on screen and append new ones, rather than
//     swapping the table — a swap would move the page under the reader every five
//     seconds;
//   - it must stop after repeated failures instead of retrying forever, and say
//     why, so a store that has gone away does not look like a quiet period;
//   - it must say how many rows arrived, which is the thing you are watching a
//     tail for.
//
// The server side is an ordinary filtered list query in the other direction; see
// TailHandler. The cursor is an opaque token the server hands back, and this
// script never parses or rebuilds it — it only echoes it. That matters: the cursor
// is the stored `ts` text, so anything that reconstructed it (a Date, a
// reformatted string) would change its length and compare wrongly against the
// column, which fails as repeated or skipped rows rather than as an error.

(function () {
  "use strict";

  // How long between polls while the tab is visible. Five seconds is fast enough
  // to feel live without making a single-user proxy's store the bottleneck.
  var POLL_MS = 5000;

  // Consecutive failures before the tail gives up. A store that is restarting
  // should recover on its own, so a couple of failures are tolerated; beyond that
  // it is a condition worth showing rather than retrying into the void.
  var MAX_FAILURES = 3;

  function setup(root) {
    // root is #rows itself: it carries the tail's configuration and it is the
    // element rows are appended to. An earlier version put the configuration on a
    // wrapper and looked for a *separate* [data-tail-body] inside root, which
    // never matched — setup() then returned silently and the button did nothing
    // at all, with no error to show for it.
    var body = root;
    var toggle = root.querySelector("[data-tail-toggle]");
    var status = root.querySelector("[data-tail-status]");
    var indicator = root.querySelector("[data-tail-indicator]");
    if (!body || !toggle || !status) {
      return;
    }

    var src = root.getAttribute("data-tail-src") || "";
    var cursor = root.getAttribute("data-tail-cursor") || "";
    var filterQs = root.getAttribute("data-tail-filter") || "";

    var on = false;
    var timer = null;
    var failures = 0;
    var inFlight = false;
    var seen = null; // Set of row ids already on screen, built on first start

    function setStatus(text, kind) {
      status.textContent = text || "";
      status.className = "tailstatus" + (kind ? " " + kind : "");
    }

    // The resting state, set before any interaction. It is a statement about the
    // view rather than about traffic: off means nothing is being followed, which
    // is different from "live but quiet" and different again from "stopped after
    // failures".
    setStatus("off");

    function setBusy(loading) {
      if (indicator) {
        indicator.style.opacity = loading ? "1" : "";
      }
    }

    // An absolute timestamp tracks when each poll last ran, which a bare "live"
    // label cannot: a tail that has silently stopped looks identical to a quiet
    // store, and the clock is what distinguishes them.
    function stamp() {
      var d = new Date();
      function p(n) { return String(n).padStart(2, "0"); }
      return p(d.getUTCHours()) + ":" + p(d.getUTCMinutes()) + ":" + p(d.getUTCSeconds());
    }

    // seenIDs reads the ids already rendered. It is built once and then
    // maintained, rather than re-read from the DOM on every poll: the point is to
    // skip rows we have, and re-scanning grows with the list.
    function buildSeen() {
      seen = {};
      var rows = body.querySelectorAll("tr[data-id]");
      for (var i = 0; i < rows.length; i++) {
        seen[rows[i].getAttribute("data-id")] = true;
      }
    }

    function poll() {
      if (!on || inFlight) {
        return;
      }
      inFlight = true;
      setBusy(true);

      var url = src + (src.indexOf("?") < 0 ? "?" : "&") +
        "cursor=" + encodeURIComponent(cursor) +
        (filterQs ? "&" + filterQs : "");

      fetch(url, {
        headers: { "Accept": "application/json" },
        cache: "no-store"
      })
        .then(function (res) {
          if (!res.ok) {
            throw new Error("HTTP " + res.status);
          }
          return res.json();
        })
        .then(function (payload) {
          failures = 0;
          inFlight = false;
          setBusy(false);

          if (payload.error) {
            setStatus("tail error: " + payload.error, "bad");
            return;
          }

          var added = append(payload);
          if (payload.cursor) {
            cursor = payload.cursor;
            root.setAttribute("data-tail-cursor", cursor);
          }

          var msg = added > 0
            ? "+" + added + " new · " + stamp()
            : "live · " + stamp();
          if (payload.truncated) {
            msg += " · burst, more than shown";
            setStatus(msg, "warn");
          } else {
            setStatus(msg, added > 0 ? "new" : "");
          }
        })
        .catch(function (err) {
          inFlight = false;
          setBusy(false);
          failures++;
          if (failures >= MAX_FAILURES) {
            stop();
            setStatus("tail stopped after " + failures + " failed polls (" + err.message + ")", "bad");
          } else {
            setStatus("poll failed (" + err.message + "), retrying", "bad");
          }
        });
    }

    // append inserts the rows the server sent, skipping any id already present.
    //
    // The skip is not paranoia: polls overlap with writes, and the server's own
    // cursor is what normally prevents a repeat. This is the second line of
    // defence, so a cursor that ever slips cannot silently duplicate rows on
    // screen — a tail showing a request twice is a wrong picture, not a cosmetic
    // bug.
    function append(payload) {
      if (!payload.rows) {
        return 0;
      }
      if (!seen) {
        buildSeen();
      }
      var tpl = document.createElement("tbody");
      tpl.innerHTML = payload.rows;

      // The empty table case: the list rendered "no requests match" and no
      // <tbody> at all, so the first appended row needs one to land in.
      var tbody = body.querySelector("tbody");
      if (!tbody) {
        var empty = body.querySelector(".empty");
        if (empty) {
          empty.remove();
        }
        var table = document.createElement("table");
        table.className = "grid";
        tbody = document.createElement("tbody");
        table.appendChild(tbody);
        body.appendChild(table);
      }

      // Prepended, because the table is newest-first and the server returns each
      // batch in that same order. Appending would put a request that arrived a
      // second ago below rows from hours ago — which is what an earlier version
      // of this did. Iterating forwards and inserting each before the current
      // first row keeps the batch's own order.
      var added = 0;
      var incoming = tpl.querySelectorAll("tr[data-id]");
      var anchor = tbody.firstChild;
      for (var i = 0; i < incoming.length; i++) {
        var id = incoming[i].getAttribute("data-id");
        if (seen[id]) {
          continue;
        }
        seen[id] = true;
        tbody.insertBefore(incoming[i], anchor);
        anchor = incoming[i].nextSibling;
        added++;
      }

      // An error row has no data-id and travels with its request's row, so it is
      // appended by the row it follows rather than skipped above.
      return added;
    }

    function start() {
      if (on) {
        return;
      }
      on = true;
      failures = 0;
      seen = null;
      toggle.textContent = "stop";
      toggle.setAttribute("aria-pressed", "true");
      // Poll immediately, so starting the tail shows whether it works rather than
      // making the reader wait a full interval to find out.
      poll();
      timer = window.setInterval(poll, POLL_MS);
    }

    function stop() {
      on = false;
      if (timer) {
        window.clearInterval(timer);
        timer = null;
      }
      toggle.textContent = "live";
      toggle.setAttribute("aria-pressed", "false");
      if (failures === 0) {
        // "off", not "": an empty status made an idle tail indistinguishable
        // from one that had silently stopped, which is the state this whole
        // control exists to make visible.
        setStatus("off");
      }
    }

    function flip() {
      if (on) {
        stop();
        setStatus("stopped");
      } else {
        start();
      }
    }

    toggle.addEventListener("click", flip);
    // The label is a second hit target for the same control, so the words are
    // clickable and not just a caption beside a small button.
    var label = root.querySelector("[data-tail-label]");
    if (label) {
      label.addEventListener("click", flip);
      label.style.cursor = "pointer";
    }

    // Pause while the tab is hidden. The interval is kept so the tail is still
    // "on" as far as the button says — it resumes by itself when the tab comes
    // back, and a reader returning to the tab should not have to restart it.
    document.addEventListener("visibilitychange", function () {
      if (!on) {
        return;
      }
      if (document.hidden) {
        if (timer) {
          window.clearInterval(timer);
          timer = null;
        }
      } else if (!timer) {
        poll();
        timer = window.setInterval(poll, POLL_MS);
      }
    });
  }

  function init() {
    var roots = document.querySelectorAll("[data-tail-src]");
    for (var i = 0; i < roots.length; i++) {
      setup(roots[i]);
    }
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
