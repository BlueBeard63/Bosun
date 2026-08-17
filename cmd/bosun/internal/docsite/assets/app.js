// Bosun docs — vanilla JS, no dependencies. Design ported from the AmberStack
// "Gantry" docs system. Handles sectioned navigation, page rendering, an
// "on this page" table of contents with scroll-spy, prev/next, a light/dark
// theme toggle, and a ⌘K command-palette search over the build-time index.
(function () {
  "use strict";

  var index = [];
  var byslug = {};
  var els = {
    nav: document.getElementById("nav"),
    article: document.getElementById("article"),
    prevnext: document.getElementById("prevnext"),
    toc: document.getElementById("toc"),
    version: document.getElementById("version"),
    searchBtn: document.getElementById("searchBtn"),
    searchKbd: document.getElementById("searchKbd"),
    themeBtn: document.getElementById("themeBtn"),
    palette: document.getElementById("palette"),
    pq: document.getElementById("pq"),
    presults: document.getElementById("presults"),
  };
  var current = "";
  var spyTargets = [];

  // ---------- theme ----------
  var SUN = '<svg viewBox="0 0 18 18" width="18" height="18" fill="none"><circle cx="9" cy="9" r="3.6" stroke="currentColor" stroke-width="1.5"/><path d="M9 1v2M9 15v2M1 9h2M15 9h2M3.3 3.3l1.4 1.4M13.3 13.3l1.4 1.4M14.7 3.3l-1.4 1.4M4.7 13.3l-1.4 1.4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>';
  var MOON = '<svg viewBox="0 0 18 18" width="18" height="18" fill="none"><path d="M15 10.5A6.5 6.5 0 0 1 7.5 3a6.5 6.5 0 1 0 7.5 7.5Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/></svg>';

  function currentTheme() {
    var t = localStorage.getItem("bosun-theme");
    if (t === "light" || t === "dark") return t;
    return window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light";
  }
  function applyTheme() {
    var t = currentTheme();
    document.documentElement.setAttribute("data-theme", t);
    els.themeBtn.innerHTML = t === "dark" ? MOON : SUN;
  }
  els.themeBtn.addEventListener("click", function () {
    localStorage.setItem("bosun-theme", currentTheme() === "dark" ? "light" : "dark");
    applyTheme();
  });
  applyTheme();

  var isMac = /Mac|iPhone|iPad/.test(navigator.platform);
  els.searchKbd.textContent = isMac ? "⌘ K" : "Ctrl K";

  // ---------- helpers ----------
  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }
  function slugFromHash() {
    var h = location.hash.replace(/^#/, "");
    var parts = h.split("#");
    return { slug: parts[0] || (index[0] && index[0].slug) || "index", anchor: parts[1] || "" };
  }
  function docPath(e) {
    return (e.section ? "" : "") + e.slug + ".md";
  }

  // ---------- sidebar ----------
  function renderNav(active) {
    els.nav.innerHTML = "";
    var lastSection = null;
    index.forEach(function (e) {
      if (e.section !== lastSection) {
        var label = document.createElement("div");
        label.className = "sec-label";
        label.textContent = e.section || "More";
        els.nav.appendChild(label);
        lastSection = e.section;
      }
      var a = document.createElement("a");
      a.className = "nav-item" + (e.slug === active ? " active" : "");
      a.href = "#" + e.slug;
      a.textContent = e.title;
      els.nav.appendChild(a);
    });
  }

  // ---------- on this page ----------
  function renderToc() {
    els.toc.innerHTML = "";
    spyTargets = [];
    var hs = els.article.querySelectorAll("h2[id], h3[id]");
    if (!hs.length) return;
    var label = document.createElement("div");
    label.className = "toc-label";
    label.textContent = "On this page";
    els.toc.appendChild(label);
    hs.forEach(function (h) {
      var a = document.createElement("a");
      a.href = "#" + current + "#" + h.id;
      a.textContent = h.textContent;
      if (h.tagName === "H3") a.className = "h3";
      a.dataset.anchor = h.id;
      a.addEventListener("click", function (ev) {
        ev.preventDefault();
        h.scrollIntoView({ behavior: "smooth" });
        history.replaceState(null, "", "#" + current + "#" + h.id);
      });
      els.toc.appendChild(a);
      spyTargets.push({ id: h.id, el: h, link: a });
    });
    updateSpy();
  }
  function updateSpy() {
    if (!spyTargets.length) return;
    var activeId = spyTargets[0].id;
    for (var i = 0; i < spyTargets.length; i++) {
      if (spyTargets[i].el.getBoundingClientRect().top - 80 <= 0) {
        activeId = spyTargets[i].id;
      }
    }
    spyTargets.forEach(function (t) {
      t.link.classList.toggle("active", t.id === activeId);
    });
  }

  // ---------- prev / next ----------
  function renderPrevNext(slug) {
    els.prevnext.innerHTML = "";
    var i = index.findIndex(function (e) { return e.slug === slug; });
    if (i < 0) return;
    if (i > 0) els.prevnext.appendChild(pnCard(index[i - 1], "prev", "← Previous"));
    else els.prevnext.appendChild(document.createElement("div"));
    if (i < index.length - 1) els.prevnext.appendChild(pnCard(index[i + 1], "next", "Next →"));
  }
  function pnCard(e, cls, label) {
    var a = document.createElement("a");
    a.className = cls;
    a.href = "#" + e.slug;
    a.innerHTML = '<span class="pn-label">' + label + '</span><span class="pn-title">' + escapeHtml(e.title) + "</span>";
    return a;
  }

  // ---------- page ----------
  function loadPage(slug, anchor) {
    fetch("/pages/" + encodeURIComponent(slug) + ".html")
      .then(function (r) { if (!r.ok) throw new Error("nf"); return r.text(); })
      .then(function (html) {
        current = slug;
        var e = byslug[slug];
        var crumb = e ? '<div class="breadcrumb">' + escapeHtml(e.section || "Docs") + "  /  " + escapeHtml(e.title) + "</div>" : "";
        els.article.innerHTML = crumb + html;
        document.title = (e ? e.title + " · " : "") + "Bosun docs";
        renderNav(slug);
        renderPrevNext(slug);
        renderToc();
        window.scrollTo(0, 0);
        if (anchor) {
          var el = document.getElementById(anchor);
          if (el) el.scrollIntoView();
        }
      })
      .catch(function () {
        current = slug;
        els.article.innerHTML = "<h1>Not found</h1><p>No page named <code>" + escapeHtml(slug) + "</code>.</p>";
        renderNav(slug);
        els.prevnext.innerHTML = "";
        els.toc.innerHTML = "";
      });
  }
  function route() {
    var s = slugFromHash();
    loadPage(s.slug, s.anchor);
  }
  window.addEventListener("hashchange", route);
  window.addEventListener("scroll", updateSpy, { passive: true });

  // ---------- search ----------
  function search(query) {
    var terms = query.toLowerCase().split(/\s+/).filter(Boolean);
    if (!terms.length) return [];
    var scored = [];
    index.forEach(function (e) {
      var title = (e.title || "").toLowerCase();
      var headings = (e.headings || []).map(function (h) { return h.text.toLowerCase(); }).join(" ");
      var body = (e.text || "").toLowerCase();
      var score = 0;
      terms.forEach(function (t) {
        score += 5 * count(title, t);
        score += 3 * count(headings, t);
        score += count(body, t);
      });
      if (score > 0) scored.push({ e: e, score: score });
    });
    scored.sort(function (a, b) { return b.score - a.score; });
    return scored.slice(0, 20);
  }
  function count(hay, needle) {
    var n = 0, i = 0;
    while ((i = hay.indexOf(needle, i)) !== -1) { n++; i += needle.length; }
    return n;
  }

  // ---------- command palette ----------
  var palOpen = false, palSel = 0, palHits = [];
  function openPalette() {
    palOpen = true;
    els.palette.hidden = false;
    els.pq.value = "";
    renderPalette("");
    els.pq.focus();
  }
  function closePalette() {
    palOpen = false;
    els.palette.hidden = true;
  }
  function renderPalette(query) {
    palHits = query ? search(query) : index.map(function (e) { return { e: e, score: 0 }; });
    palSel = 0;
    els.presults.innerHTML = "";
    if (!palHits.length) {
      els.presults.innerHTML = '<div class="empty">No matches for “' + escapeHtml(query) + '”</div>';
      return;
    }
    var lastSection = null;
    palHits.forEach(function (h, idx) {
      if (h.e.section !== lastSection) {
        var g = document.createElement("div");
        g.className = "grp";
        g.textContent = h.e.section || "More";
        els.presults.appendChild(g);
        lastSection = h.e.section;
      }
      var row = document.createElement("div");
      row.className = "hit" + (idx === palSel ? " sel" : "");
      row.dataset.idx = idx;
      row.innerHTML = '<span class="h-title">' + escapeHtml(h.e.title) + '</span><span class="h-path">' + escapeHtml(docPath(h.e)) + "</span>";
      row.addEventListener("click", function () { choose(idx); });
      row.addEventListener("mousemove", function () { setSel(idx); });
      els.presults.appendChild(row);
    });
  }
  function setSel(idx) {
    palSel = idx;
    var rows = els.presults.querySelectorAll(".hit");
    rows.forEach(function (r) { r.classList.toggle("sel", Number(r.dataset.idx) === idx); });
  }
  function moveSel(delta) {
    if (!palHits.length) return;
    palSel = (palSel + delta + palHits.length) % palHits.length;
    setSel(palSel);
    var row = els.presults.querySelector('.hit[data-idx="' + palSel + '"]');
    if (row) row.scrollIntoView({ block: "nearest" });
  }
  function choose(idx) {
    var h = palHits[idx];
    if (!h) return;
    closePalette();
    location.hash = "#" + h.e.slug;
  }

  els.searchBtn.addEventListener("click", openPalette);
  els.pq.addEventListener("input", function () { renderPalette(els.pq.value.trim()); });
  els.palette.addEventListener("click", function (ev) {
    if (ev.target.hasAttribute && ev.target.hasAttribute("data-close")) closePalette();
  });
  document.addEventListener("keydown", function (ev) {
    var mod = ev.metaKey || ev.ctrlKey;
    if (mod && ev.key.toLowerCase() === "k") { ev.preventDefault(); palOpen ? closePalette() : openPalette(); return; }
    if (!palOpen) {
      if (ev.key === "/" && document.activeElement !== els.pq) { ev.preventDefault(); openPalette(); }
      return;
    }
    if (ev.key === "Escape") { ev.preventDefault(); closePalette(); }
    else if (ev.key === "ArrowDown") { ev.preventDefault(); moveSel(1); }
    else if (ev.key === "ArrowUp") { ev.preventDefault(); moveSel(-1); }
    else if (ev.key === "Enter") { ev.preventDefault(); choose(palSel); }
  });

  // ---------- boot ----------
  fetch("/meta.json").then(function (r) { return r.json(); }).then(function (m) {
    if (m && m.version && m.version !== "dev") { els.version.textContent = m.version; els.version.hidden = false; }
  }).catch(function () {});

  fetch("/search-index.json")
    .then(function (r) { return r.json(); })
    .then(function (data) {
      index = data || [];
      index.forEach(function (e) { byslug[e.slug] = e; });
      route();
    })
    .catch(function () {
      els.article.innerHTML = "<h1>Docs unavailable</h1><p>The search index failed to load.</p>";
    });
})();
