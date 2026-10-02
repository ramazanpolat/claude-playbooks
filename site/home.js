(function () {
  "use strict";

  var data = JSON.parse(document.getElementById("showcase-data").textContent);
  var pbs = data.playbooks;
  var n = pbs.length;
  var reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var narrowMQ = window.matchMedia("(max-width: 860px)");
  var root = document.documentElement;

  function esc(s) {
    return String(s).replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
  }
  function cap(s) { return s.charAt(0).toUpperCase() + s.slice(1); }
  function icon(id) { return '<svg aria-hidden="true"><use href="#i-' + id + '"/></svg>'; }
  function plural(k, w) { return k + " " + w + (k === 1 ? "" : "s"); }

  /* ---------- theme (shared with the tour) ---------- */
  var toggle = document.querySelector(".theme");
  function lightNow() {
    var t = root.getAttribute("data-theme");
    return t ? t === "light" : window.matchMedia("(prefers-color-scheme: light)").matches;
  }
  function paintToggle() {
    if (!toggle) return;
    toggle.textContent = lightNow() ? "☾" : "☀";
    toggle.setAttribute("aria-label", lightNow() ? "Switch to dark theme" : "Switch to light theme");
  }
  paintToggle();
  if (toggle) {
    toggle.addEventListener("click", function () {
      var next = lightNow() ? "dark" : "light";
      root.setAttribute("data-theme", next);
      try { localStorage.setItem("cpb-theme", next); } catch (e) { /* per-viewer convenience only */ }
      paintToggle();
    });
  }

  /* ---------- copy buttons (the clipboard promise can hang; race it) ---------- */
  function copyText(text, btn) {
    var settled = false;
    function done() {
      if (settled) return;
      settled = true;
      var was = btn.textContent;
      btn.textContent = "copied";
      btn.classList.add("done");
      setTimeout(function () { btn.textContent = was; btn.classList.remove("done"); }, 1400);
    }
    function legacy() {
      var ta = document.createElement("textarea");
      ta.value = text; ta.style.position = "fixed"; ta.style.opacity = "0";
      document.body.appendChild(ta); ta.select();
      try { document.execCommand("copy"); } catch (e) { /* no-op */ }
      document.body.removeChild(ta);
      done();
    }
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(text).then(done, legacy);
      setTimeout(function () { if (!settled) legacy(); }, 800);
    } else { legacy(); }
  }
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest(".copy[data-copy]");
    if (b) copyText(b.getAttribute("data-copy"), b);
  });

  /* ---------- recipe highlighting ---------- */
  var KW = /\b(CREATE|ALTER|PLAYBOOK|IF|NOT|EXISTS|OR|REPLACE|ENV|ADD|SET|USE|VAR|BLOCK|MCP|SERVER|SKILL|PLUGIN|MARKETPLACE|FROM|URL|COMMAND|ARGS|HEADER|ALLOW|DENY|TOOL|MODEL|PICKER|ONLY|LABEL|DESCRIPTION|BEHAVES|AS|SANDBOX|ISOLATED|LOGIN|NO|PILOT|PROFILE)\b/g;
  function highlight(text) {
    return text.replace(/\n$/, "").split("\n").map(function (line) {
      if (/^\s*--/.test(line)) return '<span class="tk-cm">' + esc(line) + "</span>";
      return line.split(/('[^']*')/).map(function (part, i) {
        if (i % 2) return '<span class="tk-str">' + esc(part) + "</span>";
        return esc(part).replace(KW, '<span class="tk-kw">$1</span>');
      }).join("");
    }).join("\n");
  }

  /* ---------- what each notebook holds, from the data ---------- */
  function layersOf(p) {
    var sandbox = p.login === "sandbox";
    return [
      { label: "Config home", on: true, v: "own dir" },
      { label: "Environment", on: p.env.length > 0, v: p.env.length ? plural(p.env.length, "var") : "—" },
      { label: "Login", on: p.login !== "shared", v: p.login },
      { label: "Process", on: sandbox, v: sandbox ? "microVM" : "host" }
    ];
  }

  function bay(label, ico, count, body, i) {
    return '<div class="bay rise" style="--i:' + i + '"><h4>' + icon(ico) + esc(label) +
      (count == null ? "" : '<span class="n">' + count + "</span>") + "</h4>" + body + "</div>";
  }
  function chips(list, none) {
    if (!list.length) return '<span class="none">' + none + "</span>";
    return '<div class="chips">' + list.join("") + "</div>";
  }

  function overview(p) {
    var skills = chips(p.skills.map(function (s) { return '<span class="chip">' + esc(s.name) + "</span>"; }), "none");
    var plugins = chips(p.plugins.map(function (id) {
      var at = id.indexOf("@");
      return '<span class="chip">' + esc(id.slice(0, at)) + "<small>" + esc(id.slice(at)) + "</small></span>";
    }), "none");
    var mcp = p.mcp.length ? '<div class="rows">' + p.mcp.map(function (m) {
      var target = m.url ? m.url.replace(/^https?:\/\//, "") : [m.command].concat(m.args || []).join(" ");
      return '<div class="r"><span class="chip">' + esc(m.name) + '</span><span class="badge">' + esc(m.transport) +
        '</span><span class="t">' + esc(target) + "</span></div>";
    }).join("") + "</div>" : '<span class="none">none</span>';

    var model = [];
    if (p.model) model.push('<span class="chip">' + esc(p.model) + "</span>");
    if (p.model_picker) {
      model.push('<span class="none">/model picker, ' + esc(p.model_picker.mode) + ":</span>");
      p.model_picker.options.forEach(function (o) { model.push('<span class="chip">' + esc(o.label || o.model) + "</span>"); });
    }
    var tools = (p.tools.allow.length || p.tools.deny.length)
      ? '<div class="rows">' + p.tools.allow.map(function (t) { return '<div class="allow">' + esc(t) + "</div>"; }).join("") +
        p.tools.deny.map(function (t) { return '<div class="deny">' + esc(t) + "</div>"; }).join("") + "</div>"
      : '<span class="none">no rules</span>';
    var env = p.env.length ? '<div class="rows">' + p.env.map(function (v) {
      var val = v.blocked ? '<span class="blocked">blocked</span>' : '<span class="val">' + esc(v.value != null ? v.value : "<" + v.ref + ">") + "</span>";
      return '<div class="kv"><span><span class="k">' + esc(v.key) + '</span><span class="eq"> = </span>' + val +
        '</span><span class="from">' + esc(v.from) + "</span></div>";
    }).join("") + "</div>" : '<span class="none">inherits your shell</span>';

    return '<div class="bays">' +
      bay("Skills", "skill", p.skills.length, skills, 0) +
      bay("Plugins", "plugin", p.plugins.length, plugins, 1) +
      bay("MCP servers", "mcp", p.mcp.length, mcp, 2) +
      bay("Model", "model", null, model.length ? '<div class="chips">' + model.join("") + "</div>" : '<span class="none">default</span>', 3) +
      bay("Tools", "tools", p.tools.allow.length + p.tools.deny.length || null, tools, 4) +
      bay("Environment", "env", p.env.length || null, env, 5) + "</div>";
  }

  var FILE_NOTE = {
    "CLAUDE.md": "the first lines of this playbook's own CLAUDE.md",
    "settings.json": "written by cpb: tool rules, model, picker",
    ".claude.json": "Claude Code's MCP servers for this playbook",
    ".playbook": "cpb's manifest: variables, references, skills"
  };
  function files(p, idx) {
    var rootLabel = p.path + "/";
    var rows = p.files.map(function (f) {
      if (f.kind === "dir") {
        return '<li><span class="kid" style="padding-left:.45rem">' + icon("folder") + "<span>" + esc(f.name) + "/</span></span></li>" +
          f.children.map(function (c) {
            return '<li><span class="kid"><span>' + esc(c.name) + "</span>" + (c.link ? '<span class="lk">→ ' + esc(c.link) + "</span>" : "") + "</span></li>";
          }).join("");
      }
      return '<li><button type="button" data-file="' + esc(f.name) + '">' + icon("file") + "<span>" + esc(f.name) + "</span></button></li>";
    }).join("");
    return '<div class="files"><ul class="tree" role="list"><li class="root">' + esc(rootLabel) + "</li>" + rows + "</ul>" +
      '<figure class="viewer"><div class="vh"></div><pre tabindex="0"></pre><div class="note"></div></figure></div>';
  }

  function recipe(p) {
    return '<div class="recipe"><div class="vh"><span>' + esc(data.recipe) + ' · the ' + esc(p.name) + ' section</span>' +
      '<button class="copy" type="button" data-copy="' + esc(p.recipe).replace(/"/g, "&quot;") + '">copy</button></div>' +
      "<pre tabindex=\"0\">" + highlight(p.recipe) + '</pre><div class="foot">cpb APPLY showcase.cpb builds all five; applying it again changes nothing.</div></div>';
  }

  var LAUNCH_NOTE = {
    shared: "Claude Code on this config dir, with your machine's login.",
    isolated: "Claude Code on this config dir. Its own login: run /login once in it.",
    sandbox: "Always inside a microVM. Its own login, kept inside the sandbox."
  };

  function card(p, i) {
    var pathHead = p.path.slice(0, p.path.length - p.name.length);
    var li = layersOf(p).map(function (l, k) {
      return '<li class="rise' + (l.on ? " on" : "") + '" style="--i:' + (4 + k) + '"><span class="pip"></span><span>' + l.label +
        '</span><span class="v">' + esc(l.v) + "</span></li>";
    }).join("");
    var id = p.name;
    return '<section class="nb c' + (i % 5) + '" id="nb-' + id + '" role="tabpanel" aria-labelledby="tab-' + id + '" data-i="' + i + '">' +
      '<div class="spine" aria-hidden="true"><span>' + esc(id) + "</span></div>" +
      '<div class="sheet"><div class="side">' +
      '<div class="id rise" style="--i:0"><h3><i></i>' + esc(id) + "</h3>" +
      (p.launcher ? '<span class="launch">$ <b>' + esc(p.launcher) + "</b></span>" : "") + "</div>" +
      '<p class="tag rise" style="--i:1">' + esc(cap(p.tagline)) + "</p>" +
      '<div class="cfg rise" style="--i:2"><label>CLAUDE_CONFIG_DIR</label><div class="path">' + icon("folder") +
      "<span>" + esc(pathHead) + "<b>" + esc(p.name) + "</b></span></div></div>" +
      (p.launcher ? '<div class="cfg rise" style="--i:3"><label>Launch</label><div class="path"><span class="pr">$</span><span><b>' + esc(p.launcher) + "</b></span></div>" +
        '<p class="cap">' + esc(LAUNCH_NOTE[p.login]) + "</p></div>" : "") +
      '<ul class="layers">' + li + "</ul></div>" +
      '<div class="main"><div class="seg rise" role="tablist" aria-label="' + esc(id) + ' details" style="--i:2">' +
      ["Overview", "Files", "Recipe"].map(function (t, k) {
        return '<button type="button" role="tab" id="seg-' + id + "-" + k + '" aria-controls="pane-' + id + "-" + k + '" aria-selected="' + (k === 0) +
          '" tabindex="' + (k === 0 ? 0 : -1) + '" data-pane="' + k + '">' + t + "</button>";
      }).join("") + "</div>" +
      '<div class="pane" role="tabpanel" id="pane-' + id + '-0" aria-labelledby="seg-' + id + '-0">' + overview(p) + "</div>" +
      '<div class="pane" role="tabpanel" id="pane-' + id + '-1" aria-labelledby="seg-' + id + '-1" hidden>' + files(p, i) + "</div>" +
      '<div class="pane" role="tabpanel" id="pane-' + id + '-2" aria-labelledby="seg-' + id + '-2" hidden>' + recipe(p) + "</div>" +
      "</div></div></section>";
  }

  /* ---------- the stage ---------- */
  var stage = document.getElementById("stage");
  var books = stage.querySelector(".books");
  var rail = stage.querySelector(".tabs");
  var glow = stage.querySelector(".stage-glow");
  books.innerHTML = pbs.map(card).join("");
  rail.innerHTML = pbs.map(function (p, i) {
    return '<button type="button" class="tab c' + (i % 5) + '" role="tab" id="tab-' + p.name + '" aria-controls="nb-' + p.name +
      '" aria-selected="false" tabindex="-1"><span class="k">' + (i + 1) + "</span>" + esc(p.name) + "</button>";
  }).join("");

  var cards = Array.prototype.slice.call(books.querySelectorAll(".nb"));
  var tabs = Array.prototype.slice.call(rail.querySelectorAll(".tab"));
  var active = -1;
  var lockUntil = 0;
  var touched = false;
  var colors = ["#41d6a2", "#bfe36b", "#f0bd60", "#f08d73", "#93acf0"];

  function showFile(c, name) {
    var p = pbs[+c.getAttribute("data-i")];
    var view = c.querySelector(".viewer");
    view.querySelector(".vh").textContent = p.path + "/" + name;
    view.querySelector("pre").textContent = p.file_text[name] || "";
    view.querySelector(".note").textContent = FILE_NOTE[name] || "";
    Array.prototype.forEach.call(c.querySelectorAll(".tree button"), function (b) {
      b.setAttribute("aria-pressed", String(b.getAttribute("data-file") === name));
    });
  }
  cards.forEach(function (c) {
    var p = pbs[+c.getAttribute("data-i")];
    showFile(c, p.file_text["settings.json"] != null ? "settings.json" : "CLAUDE.md");
    c.querySelector(".tree").addEventListener("click", function (e) {
      var b = e.target.closest("button[data-file]");
      if (b) showFile(c, b.getAttribute("data-file"));
    });
    var seg = c.querySelector(".seg");
    var segBtns = Array.prototype.slice.call(seg.querySelectorAll("button"));
    function pick(k) {
      segBtns.forEach(function (b, j) {
        b.setAttribute("aria-selected", String(j === k));
        b.tabIndex = j === k ? 0 : -1;
        c.querySelector("#pane-" + p.name + "-" + j).hidden = j !== k;
      });
      if (k !== 0) {
        var pane = c.querySelector("#pane-" + p.name + "-" + k);
        pane.classList.remove("swap"); void pane.offsetWidth; pane.classList.add("swap");
      }
    }
    seg.addEventListener("click", function (e) {
      var b = e.target.closest("button[data-pane]");
      if (b) pick(+b.getAttribute("data-pane"));
    });
    seg.addEventListener("keydown", function (e) {
      var cur = segBtns.findIndex(function (b) { return b.getAttribute("aria-selected") === "true"; });
      var k = e.key === "ArrowRight" ? (cur + 1) % 3 : e.key === "ArrowLeft" ? (cur + 2) % 3 : -1;
      if (k >= 0) { e.preventDefault(); pick(k); segBtns[k].focus(); }
    });
  });

  function select(i, why) {
    if (i === active) return;
    active = i;
    var r = 1;
    cards.forEach(function (c, k) {
      var on = k === i;
      c.classList.toggle("is-active", on);
      c.classList.remove("lift", "tilt");
      c.style.setProperty("--rank", on ? 0 : r++);
      c.style.removeProperty("--rx"); c.style.removeProperty("--ry");
      c.querySelector(".sheet").inert = !on;
      c.setAttribute("aria-hidden", on ? "false" : "true");
      tabs[k].setAttribute("aria-selected", String(on));
      tabs[k].tabIndex = on ? 0 : -1;
    });
    if (!reduce) { void cards[i].offsetWidth; cards[i].classList.add("lift"); }
    stage.style.setProperty("--cc", colors[i % 5]);
    lockUntil = performance.now() + 520;
    moved = false;
    if (why === "kbd") tabs[i].focus({ preventScroll: true });
  }

  var dwell = null, pending = -1;
  var moved = true;                  // the pointer really moved since the last switch
  var lastX = -1, lastY = -1;
  function hoverSelect(i) {
    if (narrowMQ.matches || i === active || pending === i) return;
    clearTimeout(dwell);
    pending = i;
    dwell = setTimeout(function () {
      pending = -1;
      if (performance.now() < lockUntil || !moved) return;
      stopAuto();
      select(i, "hover");
    }, 90);
  }
  function hoverCancel() { clearTimeout(dwell); pending = -1; }
  tabs.forEach(function (t, i) {
    t.addEventListener("pointerenter", function (e) { if (e.pointerType === "mouse") hoverSelect(i); });
    t.addEventListener("pointermove", function (e) { if (e.pointerType === "mouse") hoverSelect(i); });
    t.addEventListener("pointerleave", hoverCancel);
    t.addEventListener("click", function () { stopAuto(); select(i, "click"); });
    t.addEventListener("keydown", function (e) {
      var k = -1;
      if (e.key === "ArrowDown" || e.key === "ArrowRight") k = (i + 1) % n;
      else if (e.key === "ArrowUp" || e.key === "ArrowLeft") k = (i + n - 1) % n;
      else if (e.key === "Home") k = 0;
      else if (e.key === "End") k = n - 1;
      if (k >= 0) { e.preventDefault(); stopAuto(); select(k, "kbd"); }
    });
  });
  cards.forEach(function (c, i) {
    c.addEventListener("pointerenter", function (e) { if (e.pointerType === "mouse" && i !== active) hoverSelect(i); });
    c.addEventListener("pointermove", function (e) { if (e.pointerType === "mouse" && i !== active) hoverSelect(i); });
    c.addEventListener("pointerleave", hoverCancel);
    c.addEventListener("click", function () { if (i !== active) { stopAuto(); select(i, "click"); } });
  });

  /* the fan: pointer over the stage, or any key pressed inside it */
  var fanMouse = false, fanKey = false;
  function paintFan() { stage.classList.toggle("fan", fanMouse || fanKey); }
  stage.addEventListener("pointerenter", function (e) { if (e.pointerType === "mouse") { fanMouse = true; paintFan(); } });
  stage.addEventListener("pointerleave", function () {
    fanMouse = false; paintFan();
    var c = cards[active];
    c.classList.remove("tilt"); c.style.setProperty("--rx", "0deg"); c.style.setProperty("--ry", "0deg");
  });
  stage.addEventListener("keydown", function () { fanKey = true; paintFan(); stopAuto(); });
  stage.addEventListener("focusout", function (e) {
    if (!stage.contains(e.relatedTarget)) { fanKey = false; paintFan(); }
  });

  /* tilt and glow follow the pointer */
  var raf = 0;
  stage.addEventListener("pointermove", function (e) {
    if (e.pointerType !== "mouse") return;
    if (e.clientX !== lastX || e.clientY !== lastY) { moved = true; lastX = e.clientX; lastY = e.clientY; }
    if (reduce || narrowMQ.matches) return;
    if (raf) return;
    raf = requestAnimationFrame(function () {
      raf = 0;
      var s = stage.getBoundingClientRect();
      stage.style.setProperty("--sx", ((e.clientX - s.left) / s.width * 100).toFixed(1) + "%");
      stage.style.setProperty("--sy", ((e.clientY - s.top) / s.height * 100).toFixed(1) + "%");
      var c = cards[active];
      var b = c.getBoundingClientRect();
      var nx = (e.clientX - b.left) / b.width, ny = (e.clientY - b.top) / b.height;
      if (nx < 0 || nx > 1 || ny < 0 || ny > 1) return;
      if (performance.now() > lockUntil) c.classList.add("tilt");
      c.style.setProperty("--ry", ((nx - 0.5) * 3.2).toFixed(2) + "deg");
      c.style.setProperty("--rx", ((0.5 - ny) * 2.4).toFixed(2) + "deg");
      c.style.setProperty("--mx", (nx * 100).toFixed(1) + "%");
      c.style.setProperty("--my", (ny * 100).toFixed(1) + "%");
    });
  });

  /* a gentle tour until someone takes over (never with reduced motion) */
  var auto = null;
  function startAuto() {
    if (auto || reduce || narrowMQ.matches || touched) return;
    auto = setInterval(function () { select((active + 1) % n, "auto"); }, 5200);
  }
  function stopAuto() {
    touched = true;
    if (auto) { clearInterval(auto); auto = null; }
  }
  stage.addEventListener("pointerdown", stopAuto);

  select(0, "init");
  document.documentElement.classList.add("ready");

  if ("IntersectionObserver" in window) {
    var seen = false;
    new IntersectionObserver(function (es) {
      es.forEach(function (en) {
        if (en.isIntersecting) {
          if (!seen && !reduce && !narrowMQ.matches) {
            seen = true; fanMouse = true; paintFan();           // show the whole stack once
            setTimeout(function () { fanMouse = false; paintFan(); }, 1700);
          }
          if (seen) startAuto();
        } else if (auto) { clearInterval(auto); auto = null; }
      });
    }, { threshold: 0.45 }).observe(stage);
  }

  /* ---------- the four layers: who uses what ---------- */
  var uses = {
    config: function () { return true; },
    env: function (p) { return p.env.length > 0; },
    login: function (p) { return p.login !== "shared"; },
    process: function (p) { return p.login === "sandbox"; }
  };
  Array.prototype.forEach.call(document.querySelectorAll("[data-used]"), function (el) {
    var test = uses[el.getAttribute("data-used")];
    el.innerHTML = pbs.map(function (p, i) {
      return test(p) ? '<button type="button" class="dotb c' + (i % 5) + '" data-go="' + i + '"><i></i>' + esc(p.name) + "</button>" : "";
    }).join("");
  });
  document.addEventListener("click", function (e) {
    var b = e.target.closest && e.target.closest("[data-go]");
    if (!b) return;
    stopAuto();
    select(+b.getAttribute("data-go"), "click");
    document.getElementById("playbooks").scrollIntoView({ behavior: reduce ? "auto" : "smooth", block: "start" });
  });

  /* ---------- the recipe demo: real lines from the real run ---------- */
  var term = document.getElementById("term-out");
  var folders = document.getElementById("folders");
  var okBadge = document.getElementById("again-badge");
  var dry = data.apply.dry_run;
  term.innerHTML =
    '<span class="ln"><span class="pr">$ </span><span class="cmd">cpb APPLY showcase.cpb --dry-run</span></span>' +
    dry.map(function (l, k) {
      var last = k === dry.length - 1;
      var m = l.match(/^(\S+)\s+(created|changed)\s+(.*)$/);
      return '<span class="ln" title="' + esc(l) + '">' + (m
        ? '<span class="cm">' + esc(m[1]) + '</span> <span class="' + (m[2] === "created" ? "ok" : "ch") + '">' + m[2] + "</span> " + esc(m[3])
        : '<span class="' + (last ? "ok" : "") + '">' + esc(l) + "</span>") + "</span>";
    }).join("") +
    '<span class="ln"><span class="pr">$ </span><span class="cmd">cpb APPLY showcase.cpb</span></span>' +
    '<span class="ln ok">' + esc(data.apply.applied) + "</span>" +
    '<span class="ln"><span class="pr">$ </span><span class="cmd">cpb APPLY showcase.cpb</span></span>' +
    '<span class="ln ok">' + esc(data.apply.again) + "</span>";
  folders.innerHTML = pbs.map(function (p, i) {
    return '<div class="folder c' + (i % 5) + '">' + icon("folder") + "<span>" + esc(p.path) + "</span><small>" + esc(p.login) + "</small></div>";
  }).join("");

  function runDemo() {
    var lines = Array.prototype.slice.call(term.querySelectorAll(".ln"));
    var fl = Array.prototype.slice.call(folders.querySelectorAll(".folder"));
    if (reduce) { lines.forEach(function (l) { l.classList.add("show"); }); fl.forEach(function (f) { f.classList.add("show"); }); okBadge.classList.add("show"); return; }
    var t = 0, applyAt = dry.length + 2;       // the first real APPLY line
    lines.forEach(function (l, k) {
      t += k === 0 ? 200 : k <= dry.length ? 150 : 520;
      setTimeout(function () { l.classList.add("show"); }, t);
      if (k === applyAt) {
        fl.forEach(function (f, j) { setTimeout(function () { f.classList.add("show"); }, t + 220 + j * 170); });
      }
    });
    setTimeout(function () { okBadge.classList.add("show"); }, t + 200);
  }
  var demoEl = document.getElementById("demo");
  if ("IntersectionObserver" in window) {
    var once = new IntersectionObserver(function (es) {
      if (es.some(function (e) { return e.isIntersecting; })) { once.disconnect(); runDemo(); }
    }, { threshold: 0.2 });
    once.observe(demoEl);
  } else { runDemo(); }

  /* ---------- reveal on scroll, nav spy ---------- */
  var rev = Array.prototype.slice.call(document.querySelectorAll(".reveal"));
  if ("IntersectionObserver" in window && !reduce) {
    var ro = new IntersectionObserver(function (es) {
      es.forEach(function (e) { if (e.isIntersecting) { e.target.classList.add("in"); ro.unobserve(e.target); } });
    }, { threshold: 0.12 });
    rev.forEach(function (el) { ro.observe(el); });
  } else { rev.forEach(function (el) { el.classList.add("in"); }); }

  var links = Array.prototype.slice.call(document.querySelectorAll(".top nav a[href^='#']"));
  var targets = links.map(function (a) { return document.getElementById(a.getAttribute("href").slice(1)); });
  if ("IntersectionObserver" in window) {
    var spy = new IntersectionObserver(function (es) {
      es.forEach(function (e) {
        if (!e.isIntersecting) return;
        links.forEach(function (a) { a.classList.toggle("active", a.getAttribute("href") === "#" + e.target.id); });
      });
    }, { rootMargin: "-45% 0px -50% 0px" });
    targets.forEach(function (t) { if (t) spy.observe(t); });
  }
})();
