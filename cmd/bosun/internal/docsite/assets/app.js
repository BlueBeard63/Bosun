// Bosun docs — vanilla-JS client. No external dependencies. Fetches the
// build-time search index and page fragments, does client-side full-text
// search over title/headings/body, and renders pages with hash routing.
(function () {
  "use strict";

  var index = [];
  var byslug = {};

  var q = document.getElementById("q");
  var results = document.getElementById("results");
  var nav = document.getElementById("nav");
  var content = document.getElementById("content");

  function slugFromHash() {
    var h = location.hash.replace(/^#/, "");
    var parts = h.split("#");
    return { slug: parts[0] || defaultSlug(), anchor: parts[1] || "" };
  }

  function defaultSlug() {
    return index.length ? index[0].slug : "index";
  }

  function renderNav(active) {
    nav.innerHTML = "";
    var ul = document.createElement("ul");
    index.forEach(function (e) {
      var li = document.createElement("li");
      var a = document.createElement("a");
      a.href = "#" + e.slug;
      a.textContent = e.title;
      if (e.slug === active) a.className = "active";
      li.appendChild(a);
      ul.appendChild(li);
    });
    nav.appendChild(ul);
  }

  function loadPage(slug, anchor) {
    fetch("/pages/" + encodeURIComponent(slug) + ".html")
      .then(function (r) {
        if (!r.ok) throw new Error("not found");
        return r.text();
      })
      .then(function (html) {
        content.innerHTML = html;
        document.title = (byslug[slug] ? byslug[slug].title + " · " : "") + "Bosun docs";
        renderNav(slug);
        window.scrollTo(0, 0);
        if (anchor) {
          var el = document.getElementById(anchor);
          if (el) el.scrollIntoView();
        }
      })
      .catch(function () {
        content.innerHTML = "<h1>Not found</h1><p>No page named <code>" + escapeHtml(slug) + "</code>.</p>";
        renderNav(slug);
      });
  }

  function route() {
    var s = slugFromHash();
    loadPage(s.slug, s.anchor);
  }

  // --- search (mirrors server-side Site.Search weighting) ---
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
        score += 5 * countOf(title, t);
        score += 3 * countOf(headings, t);
        score += countOf(body, t);
      });
      if (score > 0) scored.push({ e: e, score: score });
    });
    scored.sort(function (a, b) { return b.score - a.score; });
    return scored.slice(0, 12);
  }

  function countOf(hay, needle) {
    if (!needle) return 0;
    var n = 0, i = 0;
    while ((i = hay.indexOf(needle, i)) !== -1) { n++; i += needle.length; }
    return n;
  }

  function renderResults(hits, query) {
    if (!hits.length) {
      results.innerHTML = '<div class="empty">No matches for “' + escapeHtml(query) + '”</div>';
      results.hidden = false;
      return;
    }
    results.innerHTML = "";
    hits.forEach(function (h, idx) {
      var a = document.createElement("a");
      a.href = "#" + h.e.slug;
      a.className = "result" + (idx === 0 ? " sel" : "");
      a.innerHTML = "<span class=\"r-title\">" + escapeHtml(h.e.title) + "</span>" +
        "<span class=\"r-snippet\">" + escapeHtml(snippet(h.e.text, query)) + "</span>";
      a.addEventListener("click", function () { closeResults(); });
      results.appendChild(a);
    });
    results.hidden = false;
  }

  function snippet(text, query) {
    text = text || "";
    var terms = query.toLowerCase().split(/\s+/).filter(Boolean);
    var lower = text.toLowerCase();
    var idx = -1;
    terms.forEach(function (t) {
      var i = lower.indexOf(t);
      if (i >= 0 && (idx < 0 || i < idx)) idx = i;
    });
    if (idx < 0) return text.slice(0, 120);
    var start = Math.max(0, idx - 50), end = Math.min(text.length, idx + 90);
    return (start > 0 ? "…" : "") + text.slice(start, end).trim() + (end < text.length ? "…" : "");
  }

  function closeResults() {
    results.hidden = true;
    results.innerHTML = "";
  }

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, function (c) {
      return { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c];
    });
  }

  q.addEventListener("input", function () {
    var v = q.value.trim();
    if (!v) { closeResults(); return; }
    renderResults(search(v), v);
  });
  q.addEventListener("keydown", function (e) {
    if (e.key === "Escape") { closeResults(); q.blur(); }
    if (e.key === "Enter") {
      var first = results.querySelector(".result");
      if (first) { location.hash = first.getAttribute("href"); closeResults(); }
    }
  });
  document.addEventListener("keydown", function (e) {
    if (e.key === "/" && document.activeElement !== q) { e.preventDefault(); q.focus(); }
  });
  document.addEventListener("click", function (e) {
    if (!e.target.closest || !e.target.closest(".search")) closeResults();
  });

  window.addEventListener("hashchange", route);

  fetch("/search-index.json")
    .then(function (r) { return r.json(); })
    .then(function (data) {
      index = data || [];
      index.forEach(function (e) { byslug[e.slug] = e; });
      route();
    })
    .catch(function () {
      content.innerHTML = "<h1>Docs unavailable</h1><p>The search index failed to load.</p>";
    });
})();
