// Bosun docs - vanilla JS, no dependencies. Design ported from the AmberStack
// "Gantry" docs system. Handles sectioned navigation, page rendering, an
// "on this page" table of contents with scroll-spy, prev/next, a light/dark
// theme toggle, and a Cmd+K command-palette search over the build-time index.
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

  // ---------- theme (Lucide sun / moon icons) ----------
  var SUN = '<svg class="lucide" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/></svg>';
  var MOON = '<svg class="lucide" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/></svg>';

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
  els.searchKbd.textContent = isMac ? "Cmd K" : "Ctrl K";

  // Lucide icon helper (stroke-based; inherits currentColor).
  function lucide(inner, size) {
    size = size || 14;
    return '<svg class="lucide" viewBox="0 0 24 24" width="' + size + '" height="' + size +
      '" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">' + inner + "</svg>";
  }
  var ICON = {
    left: '<path d="m12 19-7-7 7-7"/><path d="M19 12H5"/>',
    right: '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>',
  };

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
  var CHEVRON = '<svg class="lucide chev" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m9 18 6-6-6-6"/></svg>';

  function collapsedSet() {
    try { return new Set(JSON.parse(localStorage.getItem("bosun-collapsed") || "[]")); }
    catch (e) { return new Set(); }
  }
  function saveCollapsed(set) {
    localStorage.setItem("bosun-collapsed", JSON.stringify([].slice.call(set)));
  }
  function appendItem(parent, e, active) {
    var a = document.createElement("a");
    a.className = "nav-item" + (e.slug === active ? " active" : "");
    a.href = "#" + e.slug;
    a.textContent = e.title;
    parent.appendChild(a);
  }

  function renderNav(active) {
    els.nav.innerHTML = "";
    var collapsed = collapsedSet();
    var lastSection = null, lastGroup = null, rail = null;
    index.forEach(function (e) {
      if (e.section !== lastSection) {
        var label = document.createElement("div");
        label.className = "sec-label";
        label.textContent = e.section || "More";
        els.nav.appendChild(label);
        lastSection = e.section;
        lastGroup = null;
        rail = null;
      }
      if (e.group) {
        if (e.group !== lastGroup) {
          lastGroup = e.group;
          var key = (e.section || "") + "/" + e.group;
          var wrap = document.createElement("div");
          wrap.className = "subcat";
          var header = document.createElement("button");
          header.type = "button";
          header.className = "subcat-header";
          header.innerHTML = CHEVRON + "<span>" + escapeHtml(e.group) + "</span>";
          rail = document.createElement("div");
          rail.className = "subcat-rail";
          var hasActive = index.some(function (x) {
            return x.section === e.section && x.group === e.group && x.slug === active;
          });
          if (collapsed.has(key) && !hasActive) wrap.classList.add("collapsed");
          header.addEventListener("click", (function (k, w) {
            return function () {
              w.classList.toggle("collapsed");
              var set = collapsedSet();
              if (w.classList.contains("collapsed")) set.add(k);
              else set.delete(k);
              saveCollapsed(set);
            };
          })(key, wrap));
          wrap.appendChild(header);
          wrap.appendChild(rail);
          els.nav.appendChild(wrap);
        }
        appendItem(rail, e, active);
      } else {
        lastGroup = null;
        rail = null;
        appendItem(els.nav, e, active);
      }
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
    if (i > 0) els.prevnext.appendChild(pnCard(index[i - 1], "prev"));
    else els.prevnext.appendChild(document.createElement("div"));
    if (i < index.length - 1) els.prevnext.appendChild(pnCard(index[i + 1], "next"));
  }
  function pnCard(e, cls) {
    var a = document.createElement("a");
    a.className = cls;
    a.href = "#" + e.slug;
    var label = cls === "prev"
      ? lucide(ICON.left) + " Previous"
      : "Next " + lucide(ICON.right);
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
        document.title = (e ? e.title + " - " : "") + "Bosun docs";
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
      els.presults.innerHTML = '<div class="empty">No matches for "' + escapeHtml(query) + '"</div>';
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
