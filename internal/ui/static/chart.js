// chart.js drives the uPlot chart on the overview page.
//
// It exists because Go's html/template cannot emit JavaScript safely: putting
// DB-derived strings (model names, aliases, epoch hashes) into an inline
// <script> block would mean either escaping them into a JS context — which the
// server cannot verify, since it does not parse the data it is emitting — or
// marking them safe. Neither is acceptable for values that came out of the
// store. So the page carries only a *URL* in a data attribute, and this script
// fetches the numbers as JSON. A URL is a string that template's own contextual
// escaper handles, and JSON parsed in the browser is data, not code.
//
// The lifecycle problem this solves: htmx swaps #pivot in and out, so a chart
// instance outlives the DOM node it was built on unless it is explicitly
// destroyed. Every swap therefore rebuilds from scratch, keyed off the node's
// own data-src, and a ResizeObserver keeps the canvas matching its container
// without a window-resize listener.
(function () {
  "use strict";

  var instances = new WeakMap();
  var observers = new WeakMap();

  // Everything Arbiter shows is UTC, and the layout header says so once. uPlot's
  // time axis renders in the *browser's* zone by default, which would make the
  // chart the one place on the page silently disagreeing with every timestamp
  // beside it — an axis reading 2:00pm over a bucket stored at 12:00 UTC, with
  // nothing saying why. The page is read against `docker logs` and `curl`
  // output, so the axis has to match them.
  //
  // uPlot ships a tzDate helper for this, but its calling convention is tied to
  // its internal tick/value pipeline and feeding it a Date produced collapsed
  // labels reading the epoch. Formatting the tick label outright is both simpler
  // and testable — the values are unix SECONDS, which is what uPlot ticks with
  // when `time: true` and no `ms`.
  function pad2(n) { return n < 10 ? "0" + n : String(n); }

  // utcTickLabel renders one axis tick in UTC. The date is appended only to the
  // first tick and whenever the UTC day changes, which is what keeps a multi-day
  // window legible without repeating the date on every tick.
  // The date is deliberately NOT in the tick labels: uPlot chooses the splits by
  // width and, on a 24h window, picks 15-minute ticks — so a date suffix would
  // repeat on every one of them and squeeze the times into each other. A day
  // bucket has nothing but a date, so there it is the label; otherwise the tick
  // is a bare UTC clock time and the day is stated once in the axis label.
  function utcTickLabel(secs, bucket) {
    var d = new Date(secs * 1000);
    var mo = pad2(d.getUTCMonth() + 1), da = pad2(d.getUTCDate());
    if (bucket === "1d") {
      return mo + "/" + da;
    }
    return pad2(d.getUTCHours()) + ":" + pad2(d.getUTCMinutes());
  }

  // utcDay labels the x axis with the UTC day the window covers, so no tick has
  // to carry a date. It reads the first and last x values, which is what the
  // window actually spans rather than what was asked for.
  function utcDay(xs) {
    function day(secs) {
      var d = new Date(secs * 1000);
      return d.getUTCFullYear() + "-" + pad2(d.getUTCMonth() + 1) + "-" + pad2(d.getUTCDate());
    }
    var real = xs.filter(function (v) { return v != null; });
    if (!real.length) return "UTC";
    var a = day(real[0]), b = day(real[real.length - 1]);
    return a === b ? a + " UTC" : a + " → " + b + " UTC";
  }

  // utcValues is uPlot's `axis.values` hook.
  //
  // The contract, which is not what it looks like: uPlot calls
  // `values(u, splits, axisIdx, foundSpace, foundIncr)` and expects a
  // *flat array of label strings, one per split*. The array-of-two-arrays form
  // belongs to the tick-*splits* function, not this one. Returning pairs here
  // made uPlot treat the whole thing as a single label, which is what drew every
  // timestamp on top of itself.
  //
  // uPlot has already chosen `splits` to fit the width, so the thinning is its
  // problem, not this function's: it labels exactly the ticks uPlot decided to
  // draw. The date is carried on the first split and on any UTC day change, so a
  // single-day window does not repeat the same date on every tick.
  function utcValues(bucket) {
    return function (u, splits) {
      return splits.map(function (split) { return utcTickLabel(split, bucket); });
    };
  }

  // One teardown for both things an instance owns. The plot and its
  // ResizeObserver have different lifetimes from the DOM node: htmx can swap
  // #chart out while both are still live, and an observer that is not
  // disconnected keeps observing a detached node and firing setSize against a
  // destroyed plot. The WeakMap entry alone does not stop it.
  function destroyAll() {
    document.querySelectorAll("#chart").forEach(function (el) {
      var ro = observers.get(el);
      if (ro) {
        ro.disconnect();
        observers.delete(el);
      }
      var u = instances.get(el);
      if (u) {
        u.destroy();
        instances.delete(el);
      }
    });
  }

  function instantiate(node) {
    if (instances.has(node)) return;
    if (typeof uPlot === "undefined") {
      node.innerHTML = '<p class="muted">chart library failed to load</p>';
      return;
    }
    var src = node.getAttribute("data-src");
    if (!src) {
      node.innerHTML = '<p class="muted">no series to draw</p>';
      return;
    }

    fetch(src, { headers: { Accept: "application/json" } })
      .then(function (r) {
        if (!r.ok) throw new Error("series " + r.status);
        return r.json();
      })
      .then(function (data) {
        // The node can be swapped between the fetch starting and finishing;
        // drawing into a detached element would leak an instance nothing can
        // find again.
        if (!document.body.contains(node)) return;

        if (!data || !data.series || data.series.length === 0 || !data.x || data.x.length === 0) {
          node.innerHTML = '<p class="muted">no series in this window</p>';
          return;
        }

        var width = node.clientWidth || 600;
        var height = 280;

        // uPlot wants [x, ...seriesValues] with nulls for gaps, so a bucket a
        // group had no traffic in is a break in the line rather than a zero —
        // zero would claim the group was idle-by-design when it simply had no
        // requests, and for a cost or latency metric that is a real difference.
        //
        // The x values are unix SECONDS, which is what the rest of Arbiter uses
        // and also what uPlot's time scale expects: with `time: true` and no
        // `ms` option, uPlot reads x as seconds. Multiplying into milliseconds
        // here does not make uPlot read milliseconds — it makes it read a
        // timestamp tens of thousands of years ahead, and the axis renders as
        // month/day labels in the year 58678. If milliseconds are ever wanted,
        // the unit belongs in `ms: 1` on the x scale, in one place, and not
        // here.
        var xValues = data.x;

        var opts = {
          width: width,
          height: height,
          legend: { show: true },
          // time: true turns on uPlot's time axis; tzDate makes its labels read
          // as UTC rather than as the browser's zone.
          scales: { x: { time: true } },
          axes: [
            // The x axis supplies its own UTC tick values and labels; see
            // utcValues. uPlot's automatic time ticks would use local time.
            { values: utcValues(data.bucket), label: utcDay(data.x), labelSize: 22, size: 48 },
            {
              // The y-axis label says which metric this is, because the same
              // chart can mean six different things.
              label: data.y_label || "",
              labelSize: 22,
            },
          ],
          series: [{ label: "time" }].concat(
            data.series.map(function (s) {
              return { label: s.label, stroke: s.color, width: 1.5, points: { show: false } };
            })
          ),
        };

        node.innerHTML = "";
        // uPlot's data shape is [xValues, ...seriesValues]. Nulls inside a
        // series are gaps, which is exactly what a bucket a group was absent
        // from should draw as — a break, not a zero.
        var seriesData = [xValues].concat(
          data.series.map(function (s) {
            return s.values;
          })
        );
        var plot = new uPlot(opts, seriesData, node);
        instances.set(node, plot);

        // Keep the canvas matched to its container. This replaces a window
        // resize listener, which would miss a layout change that does not
        // resize the window (a sidebar, a filter bar growing a line).
        if (typeof ResizeObserver !== "undefined") {
          var ro = new ResizeObserver(function () {
            // The node may have been swapped out since this fired; setting a
            // size on a destroyed plot throws rather than no-ops.
            if (!document.body.contains(node)) return;
            var w = node.clientWidth;
            if (w > 0 && plot.width !== w) plot.setSize({ width: w, height: height });
          });
          ro.observe(node);
          observers.set(node, ro);
        }
      })
      .catch(function (err) {
        if (!document.body.contains(node)) return;
        node.innerHTML = '<p class="muted">could not load the series: ' + String(err) + "</p>";
      });
  }

  function rebuild() {
    destroyAll();
    document.querySelectorAll("#chart").forEach(instantiate);
  }

  // afterSettle, not afterSwap: the canvas must be in the document and laid out
  // before clientWidth is meaningful, or the first draw is 0px wide.
  document.addEventListener("htmx:afterSettle", rebuild);
  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", rebuild);
  } else {
    rebuild();
  }
})();
