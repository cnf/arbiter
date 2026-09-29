/* transcript.js — interaction for the session transcript page (#53).
 *
 * The page arrives finished: every turn's list row and every turn's inspector
 * markup are already in the document (the inspectors sit in #inspector-src,
 * hidden, until selected). So nothing here fetches data or builds markup —
 * this script only decides what is shown, which is what keeps the page's
 * content identical whether it was rendered by Go or revealed by a click.
 *
 * One rule governs everything below: an element's turn id is its own data-id.
 * Selection, jump-to and the children strip all resolve a turn the same way,
 * so there is no second notion of "current request" to keep in sync.
 */
(function () {
  "use strict";

  var list = document.getElementById("list");
  var pane = document.getElementById("focus-pane");
  var shelf = document.getElementById("inspector-src");
  if (!list || !pane || !shelf) return;

  /* byId caches every loaded turn's inspector by turn id, keyed off whatever
   * ".inspector" markup is currently in the document — not just the shelf's
   * own children. A lazy-loaded window (#73) ships its inspectors in a
   * throwaway ".inspector-src" wrapper next to its rows inside #list, not in
   * the page's original shelf, so a document-wide query is what makes a row
   * loaded by "load more"/"load older" selectable at all; rebuildIndex is
   * re-run after every such swap (see the htmx:afterSwap listener below). */
  var byId = {};
  function rebuildIndex() {
    byId = {};
    var inspectors = document.querySelectorAll(".inspector");
    for (var i = 0; i < inspectors.length; i++) {
      byId[inspectors[i].getAttribute("data-id")] = inspectors[i];
    }
  }
  rebuildIndex();

  var currentId = null;

  /* select shows one turn's inspector. The markup is moved, not copied or
   * rebuilt, so anything the user expanded inside a turn survives leaving and
   * returning to it — the DOM element is the state.
   *
   * The turn being left goes back on the shelf first. Without that, the
   * inspector in the pane is no longer the one the cache points at and the
   * next selection empties the pane and drops the previous turn out of the
   * document for good — it would be gone until a reload. */
  function select(id) {
    var src = byId[String(id)];
    if (!src) return false;
    if (currentId === String(id)) return true;

    var prev = currentId === null ? null : byId[currentId];
    if (prev) {
      prev.hidden = true;
      shelf.appendChild(prev);
    }

    pane.innerHTML = "";
    pane.appendChild(src);
    src.hidden = false;

    var rows = list.querySelectorAll(".row, .child-row");
    for (var j = 0; j < rows.length; j++) {
      rows[j].classList.toggle("selected", rows[j].getAttribute("data-id") === String(id));
    }
    currentId = String(id);
    return true;
  }

  /* unselect puts the inspector back on the shelf, so it is still there to be
   * revealed again without a re-render. */
  function unselect() {
    if (currentId === null) return;
    var el = byId[currentId];
    if (el) {
      el.hidden = true;
      shelf.appendChild(el);
    }
    currentId = null;
  }

  /* selected() is the inspector currently in the pane, if any. */
  function selected() {
    return currentId === null ? null : byId[currentId];
  }

  /* visibleRows are the rows a user can move between with the arrow keys —
   * the list in display order, whether selected or not. Filtering only hides
   * rows, so a hidden row must not be a navigation target. */
  function visibleRows() {
    var all = list.querySelectorAll(".row, .child-row");
    var out = [];
    for (var j = 0; j < all.length; j++) {
      if (all[j].offsetParent !== null) out.push(all[j]);
    }
    return out;
  }

  /* move walks the visible rows by delta from the current one. */
  function move(delta) {
    var rows = visibleRows();
    if (!rows.length) return;
    var at = -1;
    for (var j = 0; j < rows.length; j++) {
      if (rows[j].getAttribute("data-id") === currentId) { at = j; break; }
    }
    var next = at === -1 ? (delta > 0 ? 0 : rows.length - 1) : at + delta;
    if (next < 0 || next >= rows.length) return;
    select(rows[next].getAttribute("data-id"));
    rows[next].scrollIntoView({ block: "nearest" });
    rows[next].focus({ preventScroll: true });
  }

  /* ---- click to select, on either a turn row or a nested child row ---- */
  list.addEventListener("click", function (ev) {
    var row = ev.target.closest(".row, .child-row");
    if (!row) return;
    select(row.getAttribute("data-id"));
  });

  /* jumplinks inside an inspector: "back to parent" and a parent's children
   * strip both name a turn id, so both go through select. A target that is not
   * loaded yet is reported rather than silently ignored — the reader asked for
   * something specific and deserves to know it was not found. */
  pane.addEventListener("click", function (ev) {
    var link = ev.target.closest("[data-jump]");
    if (!link) return;
    var id = link.getAttribute("data-jump");
    if (select(id)) {
      var row = list.querySelector('.row[data-id="' + id + '"], .child-row[data-id="' + id + '"]');
      if (row) row.scrollIntoView({ block: "nearest" });
      return;
    }
    say("request " + id + " is not on this page — load more turns, or use jump to #");
  });

  /* ---- keyboard: arrows walk the list, and the jump modal is Enter-driven ---
   * Arrow keys are bound on the document rather than the list because focus
   * lives on individual rows (tabindex), and on a row is exactly when the
   * reader wants to keep moving. A key typed into a field is the field's. */
  document.addEventListener("keydown", function (ev) {
    var tag = (ev.target.tagName || "").toLowerCase();
    if (tag === "input" || tag === "textarea" || ev.target.isContentEditable) return;

    if (ev.key === "ArrowDown" || ev.key === "j" || ev.key === "n") {
      ev.preventDefault();
      move(1);
      return;
    }
    if (ev.key === "ArrowUp" || ev.key === "k" || ev.key === "p") {
      ev.preventDefault();
      move(-1);
      return;
    }
    if (ev.key === "/") {
      ev.preventDefault();
      openJump();
    }
  });

  /* ---- filter: hides entries whose text does not match, in place. The list
   * is already in the DOM, so filtering is a visibility pass and the load-more
   * control stays available — a filter is not a search over unloaded turns. */
  var filter = document.getElementById("filter-text");
  if (filter) {
    filter.addEventListener("input", function () {
      var q = filter.value.trim().toLowerCase();
      var entries = list.querySelectorAll(".entry");
      for (var j = 0; j < entries.length; j++) {
        var hay = (entries[j].getAttribute("data-search") || "").toLowerCase();
        entries[j].hidden = q !== "" && hay.indexOf(q) === -1;
      }
      /* A selection that the filter just hid is dropped, or the pane would
       * show a turn the list no longer offers. */
      if (currentId !== null) {
        var row = list.querySelector('.row[data-id="' + currentId + '"], .child-row[data-id="' + currentId + '"]');
        if (row && row.offsetParent === null) unselect();
      }
    });
  }

  /* ---- the preamble modal: one shared shell, filled by moving the clicked
   * turn's own preamble markup into it (transcriptInspector.html renders one
   * hidden .preamble-src per turn that has one) — never fetched, never
   * rebuilt, so opening it costs no request no matter which turn it's for.
   * The node is moved back to its home inspector on close, the same
   * shelf-and-restore pattern select() uses, so it is still there next time
   * that turn's button is clicked or the turn is revisited. */
  var preambleModal = document.getElementById("preamble-modal");
  var preambleModalBody = document.getElementById("preamble-modal-body");
  var preambleHome = null; // the .preamble-src's own parent, to restore it to

  function openPreamble(src) {
    if (!preambleModal || !preambleModalBody) return;
    preambleHome = src.parentNode;
    preambleModalBody.appendChild(src);
    src.hidden = false;
    preambleModal.classList.add("open");
  }

  function closePreamble() {
    if (!preambleModal) return;
    var src = preambleModalBody ? preambleModalBody.firstElementChild : null;
    if (src && preambleHome) {
      src.hidden = true;
      preambleHome.appendChild(src);
    }
    preambleHome = null;
    preambleModal.classList.remove("open");
  }

  pane.addEventListener("click", function (ev) {
    var trigger = ev.target.closest("[data-preamble-trigger]");
    if (!trigger) return;
    var inspector = trigger.closest(".inspector");
    var src = inspector ? inspector.querySelector(".preamble-src") : null;
    if (src) openPreamble(src);
  });

  if (preambleModal) {
    preambleModal.addEventListener("click", function (ev) {
      if (ev.target === preambleModal || ev.target.hasAttribute("data-preamble-close")) {
        closePreamble();
        return;
      }
      var tab = ev.target.closest(".tab");
      if (!tab) return;
      var want = tab.getAttribute("data-tab");
      var tabs = preambleModal.querySelectorAll(".tab");
      for (var j = 0; j < tabs.length; j++) {
        tabs[j].classList.toggle("active", tabs[j] === tab);
      }
      var panels = preambleModal.querySelectorAll(".preamble-panel");
      for (var k = 0; k < panels.length; k++) {
        panels[k].hidden = panels[k].getAttribute("data-panel") !== want;
      }
    });
  }

  /* ---- headers toggle and "show full": in-place reveals. Both are pure
   * presentation of markup already present, which is why they need no
   * handler beyond a class. */
  pane.addEventListener("click", function (ev) {
    var toggle = ev.target.closest("[data-headers-toggle]");
    if (toggle) {
      var box = toggle.parentNode.querySelector(".headers-box");
      if (box) {
        var open = box.classList.toggle("open");
        toggle.textContent = toggle.textContent.replace(/\s*[▾▸]$/, "") + (open ? " ▾" : " ▸");
      }
      return;
    }
    var expand = ev.target.closest(".expand-link");
    if (expand) {
      var block = expand.previousElementSibling;
      if (block) block.classList.remove("clamped");
      expand.remove();
      return;
    }
  });

  /* ---- the content modal: shows a body's full text. The text is the block's
   * own siblings' content — nothing is fetched, because the server already
   * sent the whole body and only CSS hid the tail of it. */
  var contentModal = document.getElementById("content-modal");
  var contentBody = document.getElementById("content-modal-body");
  if (contentModal && contentBody) {
    contentModal.addEventListener("click", function (ev) {
      if (ev.target === contentModal || ev.target.id === "content-modal-close") {
        contentModal.classList.remove("open");
      }
    });
  }

  /* ---- jump: the modal, and the lookup it performs. Jumping to a turn that
   * is not loaded is a server question — "what is turn N of this session?" —
   * not something to answer by paging the list client-side, so it goes to the
   * jump URL the page was given and lands on the resulting page. */
  var jumpModal = document.getElementById("jump-modal");
  var jumpInput = document.getElementById("jump-input");
  var jumpStatus = document.getElementById("jump-status");
  var jumpTrigger = document.getElementById("jump-trigger");

  function say(msg, bad) {
    if (!jumpStatus) return;
    jumpStatus.textContent = msg;
    jumpStatus.classList.toggle("err", !!bad);
  }

  function openJump() {
    if (!jumpModal) return;
    jumpModal.classList.add("open");
    say("");
    if (jumpInput) {
      jumpInput.value = "";
      jumpInput.focus();
    }
  }

  function closeJump() {
    if (jumpModal) jumpModal.classList.remove("open");
  }

  if (jumpTrigger) jumpTrigger.addEventListener("click", openJump);
  if (jumpModal) {
    jumpModal.addEventListener("click", function (ev) {
      if (ev.target === jumpModal) closeJump();
    });
  }

  if (jumpInput) {
    jumpInput.addEventListener("keydown", function (ev) {
      if (ev.key === "Escape") { closeJump(); return; }
      if (ev.key !== "Enter") return;
      var n = parseInt(jumpInput.value.replace(/[^0-9]/g, ""), 10);
      if (!n || n < 1) { say("give a turn number, e.g. 42", true); return; }
      /* Already on the page: reveal it directly, which is the common case and
       * needs no round trip. */
      var row = list.querySelector('.row[data-seq="' + n + '"]');
      if (row) {
        select(row.getAttribute("data-id"));
        row.scrollIntoView({ block: "center" });
        closeJump();
        return;
      }
      say("turn #" + n + " is on a later page — loading…");
      window.location.href = jumpURL(n);
    });
  }

  /* jumpURL is the page's own URL with the turn number added. The handler
   * answers it by loading the page that contains turn N, so a jump beyond what
   * is loaded is a navigation rather than a client-side page loop. */
  function jumpURL(n) {
    var u = new URL(window.location.href);
    u.searchParams.set("seq", String(n));
    u.searchParams.delete("offset");
    return u.toString();
  }

  /* ---- silence the escape-key handler when nothing is open, so Escape in a
   * normal page does not swallow anything. */
  document.addEventListener("keydown", function (ev) {
    if (ev.key !== "Escape") return;
    if (preambleModal && preambleModal.classList.contains("open")) {
      preambleModal.classList.remove("open");
      return;
    }
    if (jumpModal && jumpModal.classList.contains("open")) closeJump();
  });

  /* ---- deep link: if the render named a turn to open with (a jump landed
   * here, or a link carried ?id=), reveal it. This is what makes a jump's
   * destination land on the turn it named rather than at the top. */
  var wanted = window.TRANSCRIPT_SELECTED;
  if (!wanted) wanted = new URL(window.location.href).searchParams.get("id");
  if (wanted && select(wanted)) {
    var row = list.querySelector('.row[data-id="' + wanted + '"], .child-row[data-id="' + wanted + '"]');
    if (row) row.scrollIntoView({ block: "center" });
  }

  /* ---- lazy-load pagination (#73): "load more"/"load older" each swap in a
   * window of turns plus that window's own inspector markup. Three things
   * have to happen that a plain htmx append/prepend does not do on its own:
   *
   *   1. byId has to be rebuilt, or a row loaded by this swap has nothing to
   *      select (the bug this whole change exists to fix).
   *   2. A prepend ("load older") changes what is above the scroll position
   *      the reader was already looking at; without compensating, the newly
   *      inserted content pushes their place in the list down and the list
   *      visibly jumps.
   *   3. Scrolling near either edge should load the next window on its own,
   *      not wait for a click — including the case where a window does not
   *      even fill the pane, so there is nothing to scroll in the first
   *      place. */

  /* The list itself never scrolls — .list-pane does (app.css's #list is an
   * unconstrained block; overflow-y: auto lives one level up). Every height/
   * scrollTop below has to act on that ancestor, not on #list, or the load
   * triggers below fire against a box that can never register a visible
   * intersection. */
  var listPane = list.closest(".list-pane") || list.parentElement;

  /* Older-prepend scroll compensation: htmx:beforeSwap/afterSwap bracket the
   * DOM mutation for a given request, so scrollHeight taken in each is a
   * before/after pair for that one swap — not a global before/after that a
   * second, unrelated swap could smuggle a value across. */
  var olderSwapHeight = null;
  list.addEventListener("htmx:beforeSwap", function (ev) {
    var btn = ev.detail && ev.detail.requestConfig && ev.detail.requestConfig.elt;
    if (btn && btn.getAttribute && btn.getAttribute("data-load") === "older") {
      olderSwapHeight = listPane.scrollHeight;
    }
  });

  list.addEventListener("htmx:afterSwap", function (ev) {
    rebuildIndex();
    var btn = ev.detail && ev.detail.requestConfig && ev.detail.requestConfig.elt;
    if (btn && btn.getAttribute && btn.getAttribute("data-load") === "older" && olderSwapHeight !== null) {
      listPane.scrollTop += listPane.scrollHeight - olderSwapHeight;
      olderSwapHeight = null;
    }
    observeLoadButtons();
  });

  /* observeLoadButtons watches whichever "load more"/"load older" buttons
   * are currently in the list and clicks one the moment it becomes visible
   * inside .list-pane — the standard infinite-scroll pattern, and what
   * covers both directions at once: a button revealed by scrolling near
   * either edge, and a button that is already visible right after a swap
   * because the loaded window didn't fill the pane (an explicit fixed
   * window size, #73, trades that possibility for not teaching the server
   * about viewport height). A fresh observer per swap is simplest here — the
   * button elements themselves are replaced on every swap, so there is
   * nothing long-lived to reuse.
   *
   * root: listPane, not the viewport — rootMargin only expands the given
   * root's own box; it does not reach into an intermediate scrollable
   * ancestor's clip the way a viewport root's margin might suggest, so a
   * button sitting just past .list-pane's visible edge reports as not
   * intersecting until root is .list-pane itself (verified empirically:
   * root: null keeps isIntersecting false for a button 30-40px below the
   * pane's bottom edge, well inside the 200px rootMargin, because the
   * observer's clip step uses the pane's unexpanded bounds — rootMargin
   * only applies to the outermost root). */
  var loadObserver = null;
  function observeLoadButtons() {
    if (loadObserver) loadObserver.disconnect();
    var targets = list.querySelectorAll('[data-load="more"], [data-load="older"]');
    if (!targets.length) return;
    loadObserver = new IntersectionObserver(function (entries) {
      entries.forEach(function (entry) {
        if (!entry.isIntersecting) return;
        loadObserver.unobserve(entry.target);
        window.htmx.trigger(entry.target, "click");
      });
    }, { root: listPane, rootMargin: "200px 0px", threshold: 0 });
    for (var i = 0; i < targets.length; i++) loadObserver.observe(targets[i]);
  }
  observeLoadButtons();
})();