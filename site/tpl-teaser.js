(function () {
  "use strict";
  /* The home page's row of templates, read from the same list the templates page
     and CI use, so the three never disagree. */
  var C = window.cpbTemplates, row = document.getElementById("tpl-row");
  if (!C || !row) return;
  function esc(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;"); }
  var html = C.TEMPLATES.map(function (t, i) {
    return '<a class="tmini c' + t.color + '" href="templates.html#' + t.id + '" style="--i:' + i + '">' +
      '<span class="plogo c' + t.color + '" aria-hidden="true"><svg><use href="#' + t.glyph + '"/></svg></span>' +
      "<span><b>" + esc(t.title) + "</b><small>" + esc(t.tagline) + "</small></span></a>";
  }).join("");
  html += '<a class="tmini own" href="templates.html#customize"><span class="plogo plus" aria-hidden="true">+</span>' +
    "<span><b>Your own</b><small>Switch things on and off.</small></span></a>";
  row.insertAdjacentHTML("afterbegin", html);
})();
