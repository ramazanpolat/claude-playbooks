(function () {
  "use strict";

  var C = window.cpbTemplates;
  var gal = document.getElementById("gal"), cz = document.getElementById("cz");
  if (!C || !gal || !cz) return;

  /* `cpb play` is not out yet: its lines stay hidden. ?play shows them, for previews. */
  var FLAGS = { play: /(^|[?&])play(=|&|$)/.test(location.search) };
  var reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var root = document.documentElement;
  var status = document.getElementById("status");

  function esc(s) { return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;"); }
  function lg(sym, cls) { return '<span class="lg' + (cls ? " " + cls : "") + '" aria-hidden="true"><svg><use href="#' + sym + '"/></svg></span>'; }
  function glm() { return '<span class="lg txt" aria-hidden="true">GLM</span>'; }
  function mark(t, size) { return '<span class="plogo ' + (size || "") + ' c' + t.color + '" aria-hidden="true"><svg><use href="#' + t.glyph + '"/></svg></span>'; }
  function say(msg) { if (status) { status.textContent = ""; setTimeout(function () { status.textContent = msg; }, 60); } }

  /* ---------- theme (shared with the other pages) ---------- */
  var toggle = document.querySelector(".theme");
  function lightNow() { var t = root.getAttribute("data-theme"); return t ? t === "light" : window.matchMedia("(prefers-color-scheme: light)").matches; }
  function paintToggle() {
    if (!toggle) return;
    toggle.textContent = lightNow() ? "☾" : "☀";
    toggle.setAttribute("aria-label", lightNow() ? "Switch to dark theme" : "Switch to light theme");
  }
  paintToggle();
  if (toggle) toggle.addEventListener("click", function () {
    var next = lightNow() ? "dark" : "light";
    root.setAttribute("data-theme", next);
    try { localStorage.setItem("cpb-theme", next); } catch (e) { /* per-viewer convenience only */ }
    paintToggle();
  });

  /* ---------- copy (the clipboard promise can hang; race it) ---------- */
  function copyText(text, btn) {
    var settled = false;
    function done() {
      if (settled) return; settled = true;
      var was = btn.getAttribute("data-label") || btn.textContent;
      btn.setAttribute("data-label", was);
      btn.textContent = "copied"; btn.classList.add("done");
      setTimeout(function () { btn.textContent = was; btn.classList.remove("done"); }, 1400);
    }
    function legacy() {
      var ta = document.createElement("textarea");
      ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.select();
      try { document.execCommand("copy"); } catch (e) { /* no-op */ }
      document.body.removeChild(ta); done();
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, legacy);
      setTimeout(function () { if (!settled) legacy(); }, 800);
    } else legacy();
  }

  /* ---------- highlighting ---------- */
  var KW = /\b(CREATE|ALTER|PLAYBOOK|IF|NOT|EXISTS|ENV|ADD|SET|USE|VAR|BLOCK|MCP|SERVER|SKILL|PLUGIN|MARKETPLACE|FROM|URL|COMMAND|ARGS|HEADER|ALLOW|DENY|TOOL|MODEL|PICKER|ONLY|APPEND|LABEL|BEHAVES|AS|SANDBOX|ISOLATED|LOGIN|NO|PILOT|PROFILE|SUBDIR|TO|APPLY|SUBDIR)\b/g;
  function hl(line) {
    if (/^--/.test(line)) {
      var m = line.match(/^(-- )([a-z-]+:)(.*)$/);
      return m ? '<span class="tk-cm">' + esc(m[1]) + '</span><span class="tk-key">' + esc(m[2]) + '</span><span class="tk-cm">' + esc(m[3]) + "</span>" : '<span class="tk-cm">' + esc(line) + "</span>";
    }
    return line.split(/('[^']*')/).map(function (part, i) {
      return i % 2 ? '<span class="tk-str">' + esc(part) + "</span>" : esc(part).replace(KW, '<span class="tk-kw">$1</span>');
    }).join("");
  }
  function codeHtml(text, prev) {
    var lines = text.replace(/\n$/, "").split("\n");
    var seen = {}; (prev || []).forEach(function (l) { seen[l] = 1; });
    var fresh = prev ? lines.filter(function (l) { return !seen[l] && l !== ""; }).length : 0;
    var flash = prev && !reduce && fresh > 0 && fresh <= lines.length * 0.6;
    return lines.map(function (l) { return '<span class="l' + (flash && !seen[l] && l !== "" ? " new" : "") + '">' + hl(l) + "</span>"; }).join("\n");
  }

  /* ---------- the facets of a template, for its card ---------- */
  function mcpDef(id) { return C.MCP.filter(function (m) { return m.id === id; })[0]; }
  function facets(d) {
    var f = [];
    d.mcp.forEach(function (id) { var m = mcpDef(id); f.push('<span class="chip" title="' + esc(m.label + ": " + m.blurb) + '">' + lg(m.sym) + esc(m.label) + "</span>"); });
    if (d.skills.length) f.push('<span class="chip">' + lg("b-anthropic") + d.skills.length + (d.skills.length === 1 ? " skill" : " skills") + "</span>");
    if (d.plugins.length) f.push('<span class="chip">' + lg("b-claude") + d.plugins.length + (d.plugins.length === 1 ? " plugin" : " plugins") + "</span>");
    var m = d.model;
    if (m.kind === "claude") f.push('<span class="chip">' + lg("b-claude") + esc(m.id) + "</span>");
    if (m.kind === "router") f.push('<span class="chip">' + glm() + "via a router</span>");
    if (d.deny.length) f.push('<span class="chip tool">' + lg("i-tools") + d.deny.length + " denied</span>");
    return f.join("");
  }
  function flagChips(d) {
    var f = [];
    if (d.sandbox) f.push('<span class="flag">SANDBOX</span>');
    if (d.isolated || d.sandbox) f.push('<span class="flag">ISOLATED LOGIN</span>');
    if (d.noProfile) f.push('<span class="flag">NO PILOT PROFILE</span>');
    return f.join("");
  }

  /* ---------- the gallery ---------- */
  var canonical = {};
  C.TEMPLATES.forEach(function (t) { canonical[t.id] = C.canonical(t.id).text; });
  gal.innerHTML = C.TEMPLATES.map(function (t, i) {
    var d = t.defaults;
    return '<article class="tcard2 c' + t.color + '" id="tpl-' + t.id + '" style="--i:' + i + '">' +
      '<header>' + mark(t, "lg") + '<div><h3>' + esc(t.title) + '</h3><p class="tg">' + esc(t.tagline) + "</p></div></header>" +
      '<p class="desc">' + esc(t.description) + "</p>" +
      '<div class="facets">' + facets(d) + "</div>" +
      (flagChips(d) ? '<div class="flags">' + flagChips(d) + "</div>" : "") +
      '<div class="actions">' +
      '<button class="btn primary sm" type="button" data-act="custom" data-id="' + t.id + '">Customize</button>' +
      '<button class="btn sm" type="button" data-act="view" data-id="' + t.id + '" aria-expanded="false" aria-controls="pv-' + t.id + '">View the file</button>' +
      '</div>' +
      '<div class="preview" id="pv-' + t.id + '" hidden><div class="pv-bar"><code>/p/' + t.id + '.cpb</code><span>' +
      '<button class="copy" type="button" data-act="copy-file" data-id="' + t.id + '">copy</button>' +
      '<a class="copy" href="p/' + t.id + '.cpb" download>download</a></span></div>' +
      '<pre class="code" tabindex="0"><code>' + codeHtml(canonical[t.id]) + "</code></pre></div>" +
      "</article>";
  }).join("");

  gal.addEventListener("click", function (e) {
    var b = e.target.closest("button[data-act]");
    if (!b) return;
    var id = b.getAttribute("data-id"), act = b.getAttribute("data-act");
    if (act === "custom") { setTemplate(id); document.getElementById("customize").scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "start" }); say("Customizing " + C.template(id).title + "."); }
    if (act === "view") {
      var pv = document.getElementById("pv-" + id), open = pv.hidden;
      pv.hidden = !open; b.setAttribute("aria-expanded", String(open)); b.textContent = open ? "Hide the file" : "View the file";
    }
    if (act === "copy-file") copyText(canonical[id], b);
  });

  /* ---------- the customizer: state ---------- */
  var sel = null, memo = { claude: null, router: null };
  var prevLines = null, urlTimer = null, touched = false;

  function setTemplate(id) {
    var mode = sel ? sel.mode : "file";
    sel = C.defaultSelection(id); sel.mode = mode;
    memo = { claude: null, router: null };
    if (sel.model.kind === "claude") memo.claude = C.clone(sel.model);
    if (sel.model.kind === "router") memo.router = C.clone(sel.model);
    prevLines = null;
    buildLists(); syncControls(); update();
  }

  function sanitize(o) {
    if (!o || typeof o !== "object" || !C.template(o.template)) return null;
    var s = C.defaultSelection(o.template);
    function ids(list, v) { return Array.isArray(v) ? list.filter(function (x) { return v.indexOf(x.id) >= 0; }).map(function (x) { return x.id; }) : s[0]; }
    if (o.mode === "file" || o.mode === "recipe") s.mode = o.mode;
    if (typeof o.name === "string") s.name = o.name.slice(0, 40);
    ["sandbox", "isolated", "noProfile"].forEach(function (k) { if (typeof o[k] === "boolean") s[k] = o[k]; });
    if (Array.isArray(o.mcp)) s.mcp = ids(C.MCP, o.mcp);
    if (Array.isArray(o.skills)) s.skills = ids(C.SKILLS, o.skills);
    if (Array.isArray(o.plugins)) s.plugins = ids(C.PLUGINS, o.plugins);
    ["allow", "deny"].forEach(function (k) { if (Array.isArray(o[k])) s[k] = o[k].filter(function (x) { return typeof x === "string"; }).slice(0, 20).map(function (x) { return x.slice(0, 100); }); });
    if (Array.isArray(o.vars)) s.vars = o.vars.filter(function (v) { return v && typeof v.key === "string" && typeof v.value === "string"; }).slice(0, 12).map(function (v) { return { key: v.key.slice(0, 64), value: v.value.slice(0, 200) }; });
    if (o.refs && typeof o.refs === "object") C.MCP.forEach(function (m) { if (m.ref && typeof o.refs[m.id] === "string") s.refs[m.id] = o.refs[m.id].slice(0, 100); });
    var m = o.model;
    if (m && m.kind === "claude" && typeof m.id === "string") s.model = { kind: "claude", id: m.id.slice(0, 80) };
    else if (m && m.kind === "default") s.model = { kind: "default", id: "" };
    else if (m && m.kind === "router") {
      s.model = { kind: "router", baseUrl: String(m.baseUrl || "").slice(0, 200), id: String(m.id || "").slice(0, 80), blockKey: m.blockKey !== false,
        tokenRef: String(m.tokenRef || "").slice(0, 100), pickerMode: m.pickerMode === "APPEND" ? "APPEND" : "ONLY",
        picker: (Array.isArray(m.picker) ? m.picker : []).slice(0, 8).map(function (e) {
          return { id: String(e.id || "").slice(0, 80), label: String(e.label || "").slice(0, 40), behavesAs: e.behavesAs ? String(e.behavesAs).slice(0, 80) : undefined };
        }) };
    }
    return s;
  }
  function encode(o) { return btoa(unescape(encodeURIComponent(JSON.stringify(o)))).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""); }
  function decode(s) { try { return JSON.parse(decodeURIComponent(escape(atob(s.replace(/-/g, "+").replace(/_/g, "/"))))); } catch (e) { return null; } }
  function shareUrl() { return location.origin + location.pathname + "?c=" + encode(sel) + "#customize"; }

  /* ---------- the customizer: markup ---------- */
  function chipToggle(attr, id, label, sym, blurb, extra) {
    return '<div class="citem"><label class="ctog" title="' + esc(blurb || "") + '"><input type="checkbox" ' + attr + '="' + esc(id) + '"><span class="cface">' + (sym ? lg(sym) : "") + '<b>' + esc(label) + "</b>" + (blurb ? "<small>" + esc(blurb) + "</small>" : "") + "</span></label>" + (extra || "") + "</div>";
  }
  function panel(n, title, hint, body) {
    return '<fieldset class="panel"><legend><span class="pn">' + n + "</span>" + esc(title) + (hint ? '<small>' + hint + "</small>" : "") + "</legend>" + body + "</fieldset>";
  }
  function sw(k, label, desc) {
    return '<label class="swrow"><input type="checkbox" class="sw" data-k="' + k + '"><span class="track" aria-hidden="true"></span><span class="swt"><b>' + label + "</b><small>" + desc + "</small></span></label>";
  }

  cz.innerHTML =
    '<div class="cz-ctrl">' +
    panel(1, "Start from", "", '<div class="pick" role="radiogroup" aria-label="Template">' + C.TEMPLATES.map(function (t) {
      return '<label class="pk c' + t.color + '"><input type="radio" name="tpl" value="' + t.id + '"><span class="pkface">' + mark(t) + "<b>" + esc(t.title) + "</b></span></label>";
    }).join("") + '</div><button class="linkbtn" type="button" data-act="reset">Reset to the template</button><a class="linkbtn jump" href="#o-fn">See the file \u2193</a>') +
    panel(2, "Name and form", "", '<div class="field"><label for="f-name">Playbook name</label><input id="f-name" type="text" data-k="name" autocomplete="off" spellcheck="false" maxlength="40"><p class="err" data-err="name"></p></div>' +
      '<div class="seg2" role="radiogroup" aria-label="Form of the output"><label><input type="radio" name="mode" data-k="mode" value="file"><span><b>A playbook file</b><small>creates the playbook, then sets it up</small></span></label>' +
      '<label><input type="radio" name="mode" data-k="mode" value="recipe"><span><b>A recipe</b><small>applies to any playbook you name</small></span></label></div>') +
    panel(3, "Safety", "", sw("sandbox", "Sandbox", "Every launch inside a microVM that sees only your folder. Needs <code>sbx</code>.") +
      sw("isolated", "Isolated login", "Shares no login with <code>~/.claude</code>: a second account, or a third-party route.") +
      sw("noProfile", "Keep ~/.pilot-profile out", "Writes the playbook's <code>CLAUDE.md</code> without those imports. Set when it is created.")) +
    panel(4, "Model", "", '<div class="seg2 three" role="radiogroup" aria-label="Model"><label><input type="radio" name="mk" data-k="mk" value="default"><span><b>Default</b><small>leave it to Claude Code</small></span></label>' +
      '<label><input type="radio" name="mk" data-k="mk" value="claude"><span><b>A Claude model</b><small>SET MODEL</small></span></label>' +
      '<label><input type="radio" name="mk" data-k="mk" value="router"><span><b>Another backend</b><small>behind a router</small></span></label></div>' +
      '<div class="sub" data-sub="claude"><label class="sel"><span>Model</span><select data-k="claudeId">' + C.MODELS.map(function (m) { return '<option value="' + m.id + '">' + esc(m.label) + "</option>"; }).join("") + "</select></label></div>" +
      '<div class="sub" data-sub="router">' +
      '<div class="field"><label for="f-url">Router URL</label><input id="f-url" type="text" data-k="routerUrl" spellcheck="false"><p class="err" data-err="routerUrl"></p></div>' +
      '<div class="field"><label for="f-rid">Model id the router serves</label><input id="f-rid" type="text" data-k="routerId" spellcheck="false"><p class="err" data-err="routerId"></p></div>' +
      '<div class="field"><label for="f-rtok">Router key, by reference <small>(optional)</small></label><input id="f-rtok" type="text" data-k="routerRef" placeholder="keychain:router-token" spellcheck="false"><p class="err" data-err="routerRef"></p></div>' +
      sw("routerBlock", "Block ANTHROPIC_API_KEY", "Removed at launch even if your shell exports it, so your own key never goes to the router.") +
      '<div class="seg2" role="radiogroup" aria-label="The /model list"><label><input type="radio" name="pm" data-k="pickerMode" value="ONLY"><span><b>/model list: only these</b><small>replaces the built-in rows</small></span></label>' +
      '<label><input type="radio" name="pm" data-k="pickerMode" value="APPEND"><span><b>/model list: add these</b><small>after the built-in rows</small></span></label></div>' +
      '<div class="rowlist" data-list="picker"></div><button class="linkbtn" type="button" data-act="add-pk">+ Add a /model entry</button>' +
      "</div>") +
    panel(5, "MCP servers", "<span>the credential is a reference, never the secret</span>", '<div class="chipgrid">' + C.MCP.map(function (m) {
      return chipToggle("data-mcp", m.id, m.label, m.sym, m.blurb,
        m.ref ? '<div class="refrow" data-refrow="' + m.id + '"><label for="ref-' + m.id + '">' + esc(m.label) + ' credential</label><input id="ref-' + m.id + '" type="text" data-ref="' + m.id + '" spellcheck="false"><p class="err" data-err="ref-' + m.id + '"></p></div>' : "");
    }).join("") + "</div>") +
    panel(6, "Skills", "<span>from <code>anthropics/skills</code></span>", '<div class="chipgrid small">' + C.SKILLS.map(function (s) { return chipToggle("data-skill", s.id, s.label, "b-anthropic", s.blurb); }).join("") + "</div>") +
    panel(7, "Plugins", "<span>from <code>claude-code-plugins</code></span>", '<div class="chipgrid small">' + C.PLUGINS.map(function (p) { return chipToggle("data-plugin", p.id, p.id, "b-claude", p.blurb); }).join("") + "</div>") +
    panel(8, "Tool rules", "<span>allow and deny, as Claude Code writes them</span>",
      '<h4 class="sh4">Allow</h4><div class="rules" data-list="allow"></div><div class="adder"><input type="text" data-add="allow" placeholder="Bash(npm run *)" spellcheck="false" aria-label="A rule to allow"><button class="btn sm" type="button" data-act="add-allow">Add</button></div><p class="err" data-err="allow"></p>' +
      '<h4 class="sh4">Deny</h4><div class="rules" data-list="deny"></div><div class="adder"><input type="text" data-add="deny" placeholder="Bash(git push *)" spellcheck="false" aria-label="A rule to deny"><button class="btn sm" type="button" data-act="add-deny">Add</button></div><p class="err" data-err="deny"></p>') +
    panel(9, "Environment", "<span>plain values only: credentials are refused</span>", '<div class="chipgrid small" data-list="varpresets"></div><div class="rowlist" data-list="vars"></div><button class="linkbtn" type="button" data-act="add-var">+ Add a variable</button><p class="err" data-err="vars"></p>') +
    "</div>" +
    '<aside class="cz-out" aria-label="The generated file"><div class="out-card">' +
    '<div class="out-head"><span class="fn" id="o-fn"></span><span class="meta" id="o-meta"></span></div>' +
    '<div class="out-actions"><button class="btn primary sm" type="button" data-act="copy">Copy</button><button class="btn sm" type="button" data-act="download">Download</button><button class="btn sm" type="button" data-act="share">Share link</button></div>' +
    '<div class="out-warn" id="o-warn" hidden></div>' +
    '<pre class="code big" tabindex="0" id="o-code"><code></code></pre>' +
    '<div class="out-err" id="o-err" hidden></div>' +
    '<div class="out-use" id="o-use"></div>' +
    "</div></aside>";

  /* ---------- the customizer: dynamic lists ---------- */
  var elName = cz.querySelector('[data-k="name"]');
  function q(sel_) { return cz.querySelector(sel_); }
  function qa(sel_) { return Array.prototype.slice.call(cz.querySelectorAll(sel_)); }

  function ruleChips(kind) {
    var preset = C.RULES[kind], mine = sel[kind], list = preset.concat(mine.filter(function (r) { return preset.indexOf(r) < 0; }));
    return list.map(function (r) {
      var on = mine.indexOf(r) >= 0, custom = preset.indexOf(r) < 0;
      return '<span class="rchip' + (on ? " on" : "") + (custom ? " custom" : "") + '"><label><input type="checkbox" data-rule="' + kind + '" value="' + esc(r) + '"' + (on ? " checked" : "") + '><code>' + esc(r) + "</code></label>" +
        (custom ? '<button type="button" data-act="rm-rule" data-kind="' + kind + '" data-rule-v="' + esc(r) + '" aria-label="Remove ' + esc(r) + '">×</button>' : "") + "</span>";
    }).join("");
  }
  function varRows() {
    var presetKeys = C.VARS.map(function (v) { return v.key; });
    return sel.vars.map(function (v, i) { return { v: v, i: i }; }).filter(function (x) { return presetKeys.indexOf(x.v.key) < 0 || false; }).map(function (x) {
      return '<div class="kvrow"><input type="text" data-vk="' + x.i + '" value="' + esc(x.v.key) + '" placeholder="NAME" aria-label="Variable name" spellcheck="false"><span>=</span>' +
        '<input type="text" data-vv="' + x.i + '" value="' + esc(x.v.value) + '" placeholder="value" aria-label="Variable value" spellcheck="false"><button type="button" data-act="rm-var" data-i="' + x.i + '" aria-label="Remove variable">×</button></div>';
    }).join("");
  }
  function pickerRows() {
    var m = sel.model;
    if (m.kind !== "router") return "";
    return (m.picker || []).map(function (e, i) {
      return '<div class="pkrow"><input type="text" data-pid="' + i + '" value="' + esc(e.id) + '" placeholder="model-id" aria-label="Model id" spellcheck="false">' +
        '<input type="text" data-plabel="' + i + '" value="' + esc(e.label || "") + '" placeholder="label" aria-label="Label" spellcheck="false">' +
        '<input type="text" data-pbeh="' + i + '" value="' + esc(e.behavesAs || "") + '" placeholder="behaves as" aria-label="Behaves as (optional)" spellcheck="false">' +
        '<button type="button" data-act="rm-pk" data-i="' + i + '" aria-label="Remove entry">×</button></div>';
    }).join("");
  }
  function buildLists() {
    q('[data-list="allow"]').innerHTML = ruleChips("allow");
    q('[data-list="deny"]').innerHTML = ruleChips("deny");
    q('[data-list="varpresets"]').innerHTML = C.VARS.map(function (v) {
      return chipToggle("data-vpreset", v.key, v.key + "=" + v.value, "i-env", v.blurb);
    }).join("");
    q('[data-list="vars"]').innerHTML = varRows();
    q('[data-list="picker"]').innerHTML = pickerRows();
  }

  /* ---------- sync controls from the state ---------- */
  function setVal(el, v) { if (el && el.value !== v && document.activeElement !== el) el.value = v; }
  function syncControls() {
    qa('input[name="tpl"]').forEach(function (r) { r.checked = r.value === sel.template; });
    setVal(elName, sel.name);
    qa('[data-k="mode"]').forEach(function (r) { r.checked = r.value === sel.mode; });
    q('[data-k="sandbox"]').checked = sel.sandbox;
    var iso = q('[data-k="isolated"]');
    iso.checked = sel.isolated || sel.sandbox; iso.disabled = sel.sandbox;
    iso.closest(".swrow").classList.toggle("implied", sel.sandbox);
    q('[data-k="noProfile"]').checked = sel.noProfile;
    var m = sel.model;
    qa('[data-k="mk"]').forEach(function (r) { r.checked = r.value === m.kind; });
    q('[data-sub="claude"]').hidden = m.kind !== "claude";
    q('[data-sub="router"]').hidden = m.kind !== "router";
    if (m.kind === "claude") q('[data-k="claudeId"]').value = m.id;
    if (m.kind === "router") {
      setVal(q('[data-k="routerUrl"]'), m.baseUrl); setVal(q('[data-k="routerId"]'), m.id); setVal(q('[data-k="routerRef"]'), m.tokenRef || "");
      q('[data-k="routerBlock"]').checked = !!m.blockKey;
      qa('[data-k="pickerMode"]').forEach(function (r) { r.checked = r.value === m.pickerMode; });
    }
    qa("[data-mcp]").forEach(function (c) { c.checked = sel.mcp.indexOf(c.getAttribute("data-mcp")) >= 0; });
    qa("[data-ref]").forEach(function (i) { setVal(i, sel.refs[i.getAttribute("data-ref")] || ""); });
    qa("[data-refrow]").forEach(function (r) { r.classList.toggle("on", sel.mcp.indexOf(r.getAttribute("data-refrow")) >= 0); });
    qa("[data-skill]").forEach(function (c) { c.checked = sel.skills.indexOf(c.getAttribute("data-skill")) >= 0; });
    qa("[data-plugin]").forEach(function (c) { c.checked = sel.plugins.indexOf(c.getAttribute("data-plugin")) >= 0; });
    qa("[data-vpreset]").forEach(function (c) {
      var k = c.getAttribute("data-vpreset");
      c.checked = sel.vars.some(function (v) { return v.key === k; });
    });
  }

  /* ---------- field errors ---------- */
  function setErr(key, msg) {
    var p = q('[data-err="' + key + '"]'); if (p) p.textContent = msg || "";
    var input = key.indexOf("ref-") === 0 ? q("#" + key) : q('[data-k="' + key + '"]');
    if (input) { if (msg) input.setAttribute("aria-invalid", "true"); else input.removeAttribute("aria-invalid"); }
  }
  function fieldErrors() {
    setErr("name", C.check.name(sel.name));
    var m = sel.model;
    setErr("routerUrl", m.kind === "router" ? C.check.url(m.baseUrl) : "");
    setErr("routerId", m.kind === "router" ? C.check.modelId(m.id) : "");
    setErr("routerRef", m.kind === "router" && m.tokenRef ? C.check.ref(m.tokenRef) : "");
    C.MCP.forEach(function (d) { if (d.ref) setErr("ref-" + d.id, sel.mcp.indexOf(d.id) >= 0 ? C.check.ref(sel.refs[d.id], d.ref) : ""); });
    var ve = "";
    sel.vars.forEach(function (v) { var e = ve || C.check.varName(v.key, v.value); if (e) ve = e; });
    setErr("vars", ve);
  }

  /* ---------- the output ---------- */
  var oFn = q("#o-fn"), oMeta = q("#o-meta"), oCode = q("#o-code code"), oWarn = q("#o-warn"), oErr = q("#o-err"), oUse = q("#o-use");
  var last = null;
  function update() {
    fieldErrors();
    var r = C.render(sel);
    last = r;
    var base = (sel.mode === "file" ? sel.name : sel.template) + ".cpb";
    oFn.textContent = r.ok ? r.file : base;
    oMeta.textContent = r.ok ? r.lines + (r.lines === 1 ? " clause" : " clauses") + " · " + (sel.mode === "file" ? "playbook file" : "recipe") : "not valid yet";
    cz.querySelectorAll(".out-actions button").forEach(function (b) { b.disabled = !r.ok; });
    if (r.ok) {
      var lines = r.text.replace(/\n$/, "").split("\n");
      oCode.innerHTML = codeHtml(r.text, prevLines);
      prevLines = lines;
      oErr.hidden = true;
      oCode.parentNode.hidden = false;
      oUse.hidden = false;
      oUse.innerHTML = usage(r);
    } else {
      oCode.parentNode.hidden = true; oUse.hidden = true;
      oErr.hidden = false;
      oErr.innerHTML = '<p><b>Not yet.</b> ' + (r.empty ? "A recipe needs at least one thing to apply. Switch something on." : "Fix these to see the file:") + "</p>" +
        (r.empty ? "" : "<ul>" + r.errors.map(function (e) { return "<li>" + esc(e) + "</li>"; }).join("") + "</ul>");
    }
    var w = r.ok ? r.warnings : [];
    oWarn.hidden = !w.length;
    oWarn.innerHTML = w.map(function (x) { return lg("i-alert") + "<span>" + esc(x) + "</span>"; }).join("");
    clearTimeout(urlTimer);
    if (!touched) return;
    urlTimer = setTimeout(function () { try { history.replaceState(null, "", location.pathname + "?c=" + encode(sel) + location.hash); } catch (e) { /* ignore */ } }, 400);
  }
  function usage(r) {
    var cmds = r.commands.map(function (c, i) {
      var isRun = i === r.commands.length - 1;
      return '<li><span class="pr">$</span><code>' + esc(c) + '</code>' + (isRun ? '<small>run it</small>' : "") + '<button class="copy" type="button" data-copy="' + esc(c) + '">copy</button></li>';
    }).join("");
    var notes = [];
    if (r.needs.length) notes.push("Needs a secret helper for " + r.needs.map(function (x) { return "<code>" + esc(x) + "</code>"; }).join(", ") + ". Store each secret yourself, then apply.");
    if (sel.sandbox) notes.push("Sandboxed launches need <code>sbx</code> (Docker Sandboxes).");
    if (sel.model.kind === "router") notes.push("Attach your router's key with an env set; it is never written to this file.");
    var play = FLAGS.play ? '<h4>Or from a URL</h4><ol class="cmds">' + C.playCommands(sel).map(function (c) { return '<li><span class="pr">$</span><code>' + esc(c) + '</code><button class="copy" type="button" data-copy="' + esc(c) + '">copy</button></li>'; }).join("") + "</ol>" : "";
    return "<h4>Use it</h4><ol class=\"cmds\">" + cmds + "</ol>" + play + (notes.length ? '<ul class="notes">' + notes.map(function (n) { return "<li>" + n + "</li>"; }).join("") + "</ul>" : "");
  }

  /* ---------- events ---------- */
  function toggleIn(list, id, on) { var i = list.indexOf(id); if (on && i < 0) list.push(id); if (!on && i >= 0) list.splice(i, 1); }
  function changed(structural) { if (structural) buildLists(); syncControls(); update(); }

  cz.addEventListener("change", function () { touched = true; }, true);
  cz.addEventListener("input", function () { touched = true; }, true);
  cz.addEventListener("click", function (e) { if (e.target.closest("[data-act]")) touched = true; }, true);

  cz.addEventListener("change", function (e) {
    var t = e.target, k = t.getAttribute("data-k");
    if (t.name === "tpl") { setTemplate(t.value); say("Switched to " + C.template(t.value).title + "."); return; }
    if (k === "mode") { sel.mode = t.value; return changed(); }
    if (k === "sandbox") { sel.sandbox = t.checked; return changed(); }
    if (k === "isolated") { sel.isolated = t.checked; return changed(); }
    if (k === "noProfile") { sel.noProfile = t.checked; return changed(); }
    if (k === "mk") {
      var cur = sel.model;
      if (cur.kind === "claude") memo.claude = cur; if (cur.kind === "router") memo.router = cur;
      if (t.value === "default") sel.model = { kind: "default", id: "" };
      if (t.value === "claude") sel.model = memo.claude || { kind: "claude", id: C.MODELS[1].id };
      if (t.value === "router") sel.model = memo.router || C.clone(C.defaultSelection("router-glm").model);
      return changed(true);
    }
    if (k === "claudeId") { sel.model.id = t.value; memo.claude = sel.model; return changed(); }
    if (k === "routerBlock") { sel.model.blockKey = t.checked; return changed(); }
    if (k === "pickerMode") { sel.model.pickerMode = t.value; return changed(); }
    if (t.hasAttribute("data-mcp")) { toggleIn(sel.mcp, t.getAttribute("data-mcp"), t.checked); return changed(); }
    if (t.hasAttribute("data-skill")) { toggleIn(sel.skills, t.getAttribute("data-skill"), t.checked); return changed(); }
    if (t.hasAttribute("data-plugin")) { toggleIn(sel.plugins, t.getAttribute("data-plugin"), t.checked); return changed(); }
    if (t.hasAttribute("data-rule")) {
      var kind = t.getAttribute("data-rule"), other = kind === "allow" ? "deny" : "allow";
      toggleIn(sel[kind], t.value, t.checked);
      if (t.checked) toggleIn(sel[other], t.value, false);
      return changed(true);
    }
    if (t.hasAttribute("data-vpreset")) {
      var key = t.getAttribute("data-vpreset"), p = C.VARS.filter(function (v) { return v.key === key; })[0];
      sel.vars = sel.vars.filter(function (v) { return v.key !== key; });
      if (t.checked) sel.vars.push({ key: p.key, value: p.value });
      return changed(true);
    }
  });

  cz.addEventListener("input", function (e) {
    var t = e.target, k = t.getAttribute("data-k");
    if (k === "name") { sel.name = t.value.trim(); return update(); }
    if (k === "routerUrl") { sel.model.baseUrl = t.value.trim(); return update(); }
    if (k === "routerId") { sel.model.id = t.value.trim(); return update(); }
    if (k === "routerRef") { sel.model.tokenRef = t.value.trim(); return update(); }
    if (t.hasAttribute("data-ref")) { sel.refs[t.getAttribute("data-ref")] = t.value.trim(); return update(); }
    if (t.hasAttribute("data-vk")) { sel.vars[+t.getAttribute("data-vk")].key = t.value.trim(); return update(); }
    if (t.hasAttribute("data-vv")) { sel.vars[+t.getAttribute("data-vv")].value = t.value.trim(); return update(); }
    if (t.hasAttribute("data-pid")) { sel.model.picker[+t.getAttribute("data-pid")].id = t.value.trim(); return update(); }
    if (t.hasAttribute("data-plabel")) { sel.model.picker[+t.getAttribute("data-plabel")].label = t.value.trim(); return update(); }
    if (t.hasAttribute("data-pbeh")) { var e2 = sel.model.picker[+t.getAttribute("data-pbeh")]; e2.behavesAs = t.value.trim() || undefined; return update(); }
  });

  function addRule(kind) {
    var input = q('[data-add="' + kind + '"]'), v = input.value.trim();
    if (!v) return;
    var msg = C.check.rule(v);
    if (!msg && sel[kind === "allow" ? "deny" : "allow"].indexOf(v) >= 0) msg = "It is already on the other list.";
    setErr(kind, msg);
    if (msg) return;
    toggleIn(sel[kind], v, true); input.value = ""; changed(true);
  }
  cz.addEventListener("keydown", function (e) {
    if (e.key === "Enter" && e.target.hasAttribute("data-add")) { e.preventDefault(); addRule(e.target.getAttribute("data-add")); }
  });

  cz.addEventListener("click", function (e) {
    var b = e.target.closest("[data-act]"); if (!b) { var cp = e.target.closest(".copy[data-copy]"); if (cp) copyText(cp.getAttribute("data-copy"), cp); return; }
    var act = b.getAttribute("data-act");
    if (act === "reset") { setTemplate(sel.template); say("Reset to the template."); }
    if (act === "add-allow") addRule("allow");
    if (act === "add-deny") addRule("deny");
    if (act === "rm-rule") { toggleIn(sel[b.getAttribute("data-kind")], b.getAttribute("data-rule-v"), false); changed(true); }
    if (act === "add-var") { sel.vars.push({ key: "", value: "" }); changed(true); var k = qa("[data-vk]").pop(); if (k) k.focus(); }
    if (act === "rm-var") { sel.vars.splice(+b.getAttribute("data-i"), 1); changed(true); }
    if (act === "add-pk") { sel.model.picker.push({ id: "", label: "" }); changed(true); var p = qa("[data-pid]").pop(); if (p) p.focus(); }
    if (act === "rm-pk") { sel.model.picker.splice(+b.getAttribute("data-i"), 1); changed(true); }
    if (act === "copy" && last && last.ok) copyText(last.text, b);
    if (act === "download" && last && last.ok) {
      var blob = new Blob([last.text], { type: "text/plain;charset=utf-8" }), a = document.createElement("a");
      a.href = URL.createObjectURL(blob); a.download = last.file; document.body.appendChild(a); a.click(); document.body.removeChild(a);
      setTimeout(function () { URL.revokeObjectURL(a.href); }, 1000);
      say("Downloaded " + last.file + ".");
    }
    if (act === "share") copyText(shareUrl(), b);
  });

  document.addEventListener("click", function (e) {
    var cp = e.target.closest(".copy[data-copy-from]");
    if (cp) copyText(document.getElementById(cp.getAttribute("data-copy-from")).textContent, cp);
  });

  /* ---------- start ---------- */
  var params = new URLSearchParams(location.search), fromUrl = params.get("c") ? sanitize(decode(params.get("c"))) : null;
  var wanted = params.get("t") && C.template(params.get("t")) ? params.get("t") : (location.hash.length > 1 && C.template(location.hash.slice(1)) ? location.hash.slice(1) : null);
  if (fromUrl) { sel = fromUrl; if (sel.model.kind === "claude") memo.claude = sel.model; if (sel.model.kind === "router") memo.router = sel.model; buildLists(); syncControls(); update(); }
  else setTemplate(wanted || C.TEMPLATES[0].id);
  if (wanted && !fromUrl) setTimeout(function () { document.getElementById("customize").scrollIntoView(); }, 50);

  /* the how-to commands carry this site's own address */
  var curl = document.getElementById("curl-ex");
  if (curl) curl.textContent = "curl -fsSL " + location.origin + "/p/code-reviewer.cpb -o reviewer.cpb";

  /* nav spy */
  var links = Array.prototype.slice.call(document.querySelectorAll(".top nav a[href^='#']"));
  if ("IntersectionObserver" in window) {
    var spy = new IntersectionObserver(function (es) {
      es.forEach(function (en) { if (en.isIntersecting) links.forEach(function (a) { a.classList.toggle("active", a.getAttribute("href") === "#" + en.target.id); }); });
    }, { rootMargin: "-45% 0px -50% 0px" });
    links.forEach(function (a) { var t = document.getElementById(a.getAttribute("href").slice(1)); if (t) spy.observe(t); });
    var rev = new IntersectionObserver(function (es) { es.forEach(function (en) { if (en.isIntersecting) { en.target.classList.add("in"); rev.unobserve(en.target); } }); }, { threshold: 0.1 });
    Array.prototype.forEach.call(document.querySelectorAll(".tcard2, .steps3 li, .facts > div"), function (el) { el.classList.add("rv"); if (reduce) el.classList.add("in"); else rev.observe(el); });
  }
})();
