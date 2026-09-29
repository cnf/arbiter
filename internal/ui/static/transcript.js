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

  /* byId caches the shelf's inspectors by turn id. Built once, from the DOM,
   * rather than queried per click: a conversation is small enough to index. */
  var byId = {};
  var inspectors = shelf.querySelectorAll(".inspector");
  for (var i = 0; i < inspectors.length; i++) {
    byId[inspectors[i].getAttribute("data-id")] = inspectors[i];
  }

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
})();