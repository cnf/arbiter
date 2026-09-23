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
    // grouped mirrors the server's rendering mode: in a grouped table a polled
    // row either joins an existing line (bumping its count) or becomes its own
    // line, and prepending it regardless would draw a second line for a group
    // already on screen. The server sends `grouped` on every poll too, so a mode
    // change mid-tail cannot desynchronise the two.
    var grouped = root.hasAttribute("data-tail-grouped");

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
      var rows = body.querySelectorAll(".reqrow[data-id]");
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

          // The server's mode wins: if it disagrees with what this page was
          // rendered with, follow the server. A poll's payload is always rows, so
          // the client is the only place the two views can be reconciled.
          if (typeof payload.grouped === "boolean") {
            grouped = payload.grouped;
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

    // append inserts the rows the server sent, skipping any id already present, and
    // places each one according to the table's rendering mode.
    //
    // The id skip is not paranoia: polls overlap with writes, and the server's own
    // cursor is what normally prevents a repeat. This is the second line of
    // defence, so a cursor that ever slips cannot silently duplicate rows on
    // screen — a tail showing a request twice is a wrong picture, not a cosmetic
    // bug.
    //
    // Placement differs by mode and both branches are load-bearing:
    //
    //   - flat: prepend each row and keep the batch's own order. The table is
    //     newest-first and the server returns each batch in that same order, so a
    //     new row belongs at the top.
    //   - grouped: a row whose group key is already on screen belongs *inside*
    //     that line — bump its count and advance its timestamp, because the line
    //     shows its newest request. A row with no key (or a key not on screen) is
    //     a line of its own and is prepended in order, exactly as flat does.
    function append(payload) {
      if (!payload.rows || !payload.rows.length) {
        return 0;
      }
      if (!seen) {
        buildSeen();
      }

      // The empty-list case: the list rendered "no requests match" with no
      // .reqhead/.reqbody at all, so the first appended row needs a body (and
      // the column header) to land in.
      var reqbody = body.querySelector(".reqbody");
      if (!reqbody) {
        var empty = body.querySelector(".empty");
        if (empty) {
          empty.remove();
        }
        if (!body.querySelector(".reqhead")) {
          var head = document.createElement("div");
          head.className = "reqhead";
          head.setAttribute("role", "row");
          head.innerHTML =
            '<span class="c-bar" aria-hidden="true"></span>' +
            '<span class="c-time">time</span>' +
            '<span class="c-chain">provider / model</span>' +
            '<span class="c-session">session</span>' +
            '<span class="c-cost">tokens / cost / latency</span>';
          body.appendChild(head);
        }
        reqbody = document.createElement("div");
        reqbody.className = "reqbody";
        body.appendChild(reqbody);
      }

      var added = 0;
      var anchor = reqbody.firstChild;
      for (var i = 0; i < payload.rows.length; i++) {
        var id = payload.rows[i].id;
        if (id && seen[id]) {
          continue;
        }
        if (id) {
          seen[id] = true;
        }

        var row = rowFromHTML(payload.rows[i].html);
        if (!row) {
          continue;
        }

        var key = payload.rows[i].key || "";
        if (grouped && key && bumpLine(reqbody, key, row)) {
          added++;
          continue;
        }

        // The batch arrives newest-first, so inserting each before the current
        // first row keeps the batch's own order.
        reqbody.insertBefore(row, anchor);
        anchor = row.nextSibling;
        added++;
      }

      return added;
    }

    // rowFromHTML turns one response row into a .reqrow element. It returns
    // null rather than throwing on malformed markup, so one bad row cannot
    // take out a poll: the alternative is an uncaught error that stops the
    // tail.
    function rowFromHTML(html) {
      if (!html) {
        return null;
      }
      var holder = document.createElement("div");
      holder.innerHTML = html;
      return holder.firstElementChild;
    }

    // bumpLine folds a polled row into the line whose data-key matches, moving the
    // line to the top because the row it now shows is the newest of its group.
    //
    // Returns false when no such line is on screen, which means the caller should
    // render the row as its own line. The count is re-read from the existing badge
    // rather than rebuilt from text, and the line is re-anchored: leaving it where
    // it was would put a request from a second ago below lines it is newer than,
    // which is the ordering bug this list's live view already had once.
    //
    // A line that was alone has no count badge yet — the page only renders one for
    // a run — so the first row that joins it has to create the badge. That is the
    // user's `A` then `A` case: without this the line stays count-less and the
    // second turn is invisible as a repeat, which is the thing being grouped.
    function bumpLine(reqbody, key, row) {
      var line = reqbody.querySelector('.reqrow[data-key="' + key + '"]');
      if (!line) {
        return false;
      }
      var badge = line.querySelector("[data-count]");
      if (!badge) {
        badge = makeCountBadge(line);
      }
      if (badge) {
        var n = parseInt(badge.getAttribute("data-count"), 10);
        if (!isNaN(n)) {
          n++;
          badge.setAttribute("data-count", String(n));
          badge.textContent = n + "×";
          badge.title = "this line stands for " + n + " streamed requests on the page loaded — open them as individual rows";
        }
      }
      reqbody.insertBefore(line, reqbody.firstChild);
      return true;
    }

    // makeCountBadge turns a line that stood for a single request into a counted
    // run, mirroring the markup the server renders for a collapsed line: the badge
    // goes into the time cell, replacing the "ago" subline that filled it.
    //
    // It starts at 1 and lets the caller increment it, so the count and the badge
    // are updated in exactly one place.
    function makeCountBadge(line) {
      var cell = line.querySelector(".c-time");
      if (!cell) {
        return null;
      }
      var sub = cell.querySelector("span.sub");
      var badge = document.createElement("a");
      badge.className = "count";
      badge.setAttribute("data-count", "1");
      badge.textContent = "1×";
      // The href is the flat view of this line's requests. The client cannot build
      // the server's query (it does not know the session, status or kind the key
      // was folded from) so the link is left to the page's own rendering: a line
      // that became a run on screen points at the full list, which is honest about
      // being less narrow than a server-rendered count's link.
      badge.href = "/admin/ui/requests?flat=1";
      if (sub) {
        cell.insertBefore(badge, sub);
      } else {
        cell.appendChild(badge);
      }
      line.classList.add("run");
      return badge;
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
