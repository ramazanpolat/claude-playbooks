(function () {
  "use strict";

  /* Where each agent runs, and what it can reach. The runtimes and the launch
     commands come from site-tools/runtimes.json (checked against this cpb's own
     flags and backends); what an agent reaches comes from its playbook: its
     model backend, its MCP servers, the folders its launch mounts. Where two
     agents reach the same thing, they can meet. */
  var K = window.cpb;
  var root = document.getElementById("rt");
  if (!K || !root) return;

  var esc = K.esc, pbs = K.pbs, plan = K.data.runtimes;
  var zonesEl = document.getElementById("rt-zones");
  var svcEl = document.getElementById("rt-services");
  var detail = document.getElementById("rt-detail");
  var svg = root.querySelector(".rt-lines");
  var SVGNS = "http://www.w3.org/2000/svg";

  var byName = {};
  pbs.forEach(function (p, i) { byName[p.name] = { p: p, i: i }; });
  var launchOf = {};
  plan.launches.forEach(function (l) { launchOf[l.playbook] = l; });

  var ZONE = {
    host: { sym: "g-host", text: "Claude Code runs as you, with this playbook's own config directory, environment and login. Every other layer is yours to add." },
    sbx: { sym: "b-docker", text: "A Docker Sandboxes microVM with its own kernel, filesystem and network. It sees your working directory, the playbook's directory and any mount you add. API keys stay on the host: a host-side proxy injects them." },
    openshell: { sym: "g-shell", text: "Linux with Docker. A container confined by Landlock and seccomp, with no network unless a rule allows it. Keys are bound to one endpoint." },
    remote: { sym: "g-remote", text: "The same sandboxed launch, run on another machine over ssh. Its sandbox, its login and its keys live on that machine." },
    throwaway: { sym: "g-ghost", text: "An ad-hoc session whose config directory is a throwaway folder. The folder is deleted when the session ends, and the sandbox with it if you used one." }
  };
  var PRETTY = { github: "GitHub", linear: "Linear", notion: "Notion", sentry: "Sentry", "cloudflare-docs": "Cloudflare docs", playwright: "Playwright", context7: "Context7" };
  function pretty(n) { return PRETTY[n] || n.replace(/-/g, " ").replace(/^./, function (c) { return c.toUpperCase(); }); }

  /* ---------- what each agent reaches ---------- */
  var services = {};            // id -> {id, kind, label, sub, logo, users[]}
  var order = [];               // display order
  function svc(id, kind, label, sub, logo) {
    if (!services[id]) { services[id] = { id: id, kind: kind, label: label, sub: sub, logo: logo, users: [] }; order.push(id); }
    return services[id];
  }
  function use(id, name) { if (services[id].users.indexOf(name) < 0) services[id].users.push(name); }

  var links = [];               // {a, s}
  pbs.forEach(function (p) {
    var base = ((p.env || []).filter(function (v) { return v.key === "ANTHROPIC_BASE_URL"; })[0] || {}).value;
    var be;
    if (base) {
      var host = base.replace(/^https?:\/\//, "").replace(/\/.*$/, "");
      be = svc("be-router", "Model backend", "Your router", host, K.lg("g-router") + '<span class="lg txt">GLM</span>' + K.lg("b-deepseek"));
    } else {
      be = svc("be-anthropic", "Model backend", "Anthropic API", "the default", K.lg("b-anthropic"));
    }
    use(be.id, p.name); links.push({ a: p.name, s: be.id });
  });
  var mcpIds = [];
  pbs.forEach(function (p) {
    (p.mcp || []).forEach(function (m) {
      var id = "mcp-" + m.name;
      var sub = m.url ? m.transport + " · " + m.url.replace(/^https?:\/\//, "").replace(/\/.*$/, "") : m.transport + " · " + [m.command].concat(m.args || []).join(" ");
      if (!services[id]) mcpIds.push(id);
      svc(id, "MCP server", pretty(m.name), sub, K.lg(K.mcpSym(m.name)));
      use(id, p.name); links.push({ a: p.name, s: id });
    });
  });
  plan.launches.forEach(function (l) {
    var re = /--mount\s+(\S+)/g, m;
    while ((m = re.exec(l.cmd))) {
      var ro = /:ro$/.test(m[1]);
      var path = m[1].replace(/:ro$/, "");
      var id = "mt-" + path;
      svc(id, "Shared folder", path, ro ? "read-only mount" : "mount", K.lg("g-share"));
      use(id, l.playbook); links.push({ a: l.playbook, s: id });
    }
  });
  // backends first, then MCP servers in the order the agents appear, then folders
  function first(id) { return Math.min.apply(null, services[id].users.map(function (u) { return byName[u] ? byName[u].i : 99; })); }
  var sorted = order.filter(function (id) { return services[id].kind === "Model backend"; })
    .concat(order.filter(function (id) { return services[id].kind === "MCP server"; }).sort(function (a, b) { return first(a) - first(b); }))
    .concat(order.filter(function (id) { return services[id].kind === "Shared folder"; }));

  /* ---------- the runtimes and the agents in them ---------- */
  function reach(name) {
    return links.filter(function (l) { return l.a === name; }).map(function (l) { return services[l.s]; });
  }
  zonesEl.innerHTML = plan.zones.map(function (z) {
    var agents = plan.launches.filter(function (l) { return l.zone === z.id; });
    var gate = z.kind === "sandbox" || z.kind === "remote";
    return '<div class="zone z-' + esc(z.id) + '" data-zone="' + esc(z.id) + '">' +
      '<button type="button" class="zhead" data-zone="' + esc(z.id) + '">' + K.lg(ZONE[z.id].sym, "zi") + "<b>" + esc(z.label) + '</b><span class="ztag">' + esc(z.tag) + "</span></button>" +
      '<div class="zbody">' + agents.map(function (l) {
        var e = byName[l.playbook], p = e.p;
        return '<button type="button" class="agent c' + K.cidx(p, e.i) + (p.ephemeral ? " ghost" : "") + '" data-pb="' + esc(p.name) + '">' + K.plogo(p, "lg") +
          '<span class="an"><b>' + esc(p.name) + "</b><code>" + esc(l.cmd) + "</code></span>" +
          '<span class="reach">' + reach(p.name).map(function (s) { return '<span class="chip">' + s.logo + esc(s.label) + "</span>"; }).join("") + "</span></button>";
      }).join("") + "</div>" +
      (gate ? '<span class="gate" title="API keys are injected here, by a host-side proxy">' + K.lg("g-lock") + "<small>keys</small></span>" : "") + "</div>";
  }).join("");

  svcEl.innerHTML = ["Model backend", "MCP server", "Shared folder"].map(function (kind) {
    var ids = sorted.filter(function (id) { return services[id].kind === kind; });
    return '<div class="sgroup"><h3>' + esc(kind === "MCP server" ? "MCP servers" : kind === "Shared folder" ? "Shared folders" : "Model backends") + "</h3>" + ids.map(function (id) {
      var s = services[id], shared = s.users.length > 1;
      return '<button type="button" class="svc' + (shared ? " shared" : "") + '" data-svc="' + esc(id) + '">' + '<span class="slogo">' + s.logo + "</span>" +
        '<span class="sn"><b>' + esc(s.label) + "</b><small>" + esc(s.sub) + "</small></span>" +
        (shared ? '<span class="shr" title="shared by ' + esc(s.users.join(" and ")) + '">' + s.users.length + "×</span>" : "") + "</button>";
    }).join("") + "</div>";
  }).join("");

  /* ---------- the lines ---------- */
  function pt(el, side) {
    var r = el.getBoundingClientRect(), R = root.getBoundingClientRect();
    return { x: (side === "right" ? r.right : side === "left" ? r.left : (r.left + r.right) / 2) - R.left, y: (r.top + r.bottom) / 2 - R.top };
  }
  var pathEls = [];
  function layout() {
    while (svg.firstChild) svg.removeChild(svg.firstChild);
    pathEls = [];
    if (K.narrow.matches) return;
    var R = root.getBoundingClientRect();
    svg.setAttribute("viewBox", "0 0 " + R.width + " " + R.height);
    svg.setAttribute("width", R.width); svg.setAttribute("height", R.height);
    links.forEach(function (l, k) {
      var agent = zonesEl.querySelector('.agent[data-pb="' + l.a + '"]');
      var target = svcEl.querySelector('.svc[data-svc="' + l.s + '"]');
      if (!agent || !target) return;
      var zone = agent.closest(".zone"), gate = zone.querySelector(".gate");
      var p = byName[l.a], a = pt(agent, "right"), t = pt(target, "left"), start = a, d = "";
      if (gate) {
        var g = pt(gate, "right");
        d = "M" + a.x + " " + a.y + " L" + (g.x - 14) + " " + a.y + " ";
        start = { x: g.x, y: a.y };
        d += "L" + start.x + " " + start.y + " ";
      } else { d = "M" + a.x + " " + a.y + " "; }
      var dx = Math.max(40, (t.x - start.x) * 0.5);
      d += "C" + (start.x + dx) + " " + start.y + " " + (t.x - dx) + " " + t.y + " " + t.x + " " + t.y;
      var path = document.createElementNS(SVGNS, "path");
      path.setAttribute("d", d);
      path.setAttribute("class", "link c" + K.cidx(p.p, p.i) + (gate ? " sealed" : "") + (p.p.ephemeral ? " ghost" : ""));
      path.dataset.a = l.a; path.dataset.s = l.s;
      svg.appendChild(path);
      var dot = document.createElementNS(SVGNS, "circle");
      dot.setAttribute("r", "3.2");
      dot.setAttribute("class", "pkt c" + K.cidx(p.p, p.i));
      dot.dataset.a = l.a; dot.dataset.s = l.s;
      if (!K.reduce) {
        var m = document.createElementNS(SVGNS, "animateMotion");
        m.setAttribute("dur", (3.2 + (k % 5) * 0.45) + "s");
        m.setAttribute("begin", ((k * 0.37) % 3) + "s");
        m.setAttribute("repeatCount", "indefinite");
        m.setAttribute("path", d);
        dot.appendChild(m);
      } else { dot.setAttribute("cx", t.x); dot.setAttribute("cy", t.y); dot.style.display = "none"; }
      svg.appendChild(dot);
      pathEls.push(path, dot);
    });
    apply();
  }

  /* ---------- hover, focus, pin ---------- */
  var cur = null, pinned = null;
  function sharedWith(name) {
    var out = [];
    reach(name).forEach(function (s) {
      s.users.forEach(function (u) { if (u !== name) out.push({ who: u, at: s }); });
    });
    return out;
  }
  function describe(sel) {
    if (!sel) return '<p class="idle">Hover or focus an agent, a runtime or a service. Click to pin it.</p>';
    if (sel.type === "agent") {
      var l = launchOf[sel.id], z = plan.zones.filter(function (x) { return x.id === l.zone; })[0], e = byName[sel.id];
      var meet = sharedWith(sel.id);
      var met = meet.length ? meet.filter(function (m) { return m.at.kind !== "Model backend"; }) : [];
      var byWho = {};
      met.forEach(function (m) { (byWho[m.who] = byWho[m.who] || []).push(m.at.label); });
      return '<div class="d-head">' + K.plogo(e.p, "lg") + "<div><b>" + esc(sel.id) + "</b><span>runs in " + esc(z.label.toLowerCase()) + " <em>" + esc(z.tag) + "</em></span></div></div>" +
        '<div class="d-cmd"><span class="pr">$</span><code>' + esc(l.cmd) + '</code><button class="copy" type="button" data-copy="' + esc(l.cmd) + '">copy</button></div>' +
        "<p>" + esc(ZONE[z.id].text) + "</p>" +
        '<p class="d-reach"><b>Reaches</b> ' + reach(sel.id).map(function (s) { return '<span class="chip">' + s.logo + esc(s.label) + "</span>"; }).join("") + "</p>" +
        (Object.keys(byWho).length ? '<p class="d-meet"><b>Can meet</b> ' + Object.keys(byWho).map(function (w) { return "<code>" + esc(w) + "</code> at " + esc(byWho[w].join(", ")); }).join("; ") + ".</p>"
          : '<p class="d-meet"><b>Can meet</b> nobody: it shares no MCP server or folder with another agent here.</p>');
    }
    if (sel.type === "svc") {
      var s = services[sel.id];
      return '<div class="d-head"><span class="slogo big">' + s.logo + "</span><div><b>" + esc(s.label) + "</b><span>" + esc(s.kind) + " · " + esc(s.sub) + "</span></div></div>" +
        "<p>" + (s.users.length > 1 ? "Used by " + s.users.map(function (u) { return "<code>" + esc(u) + "</code>"; }).join(" and ") + ". Shared, so it is a place where those agents can meet." : "Reached only by <code>" + esc(s.users[0]) + "</code>.") + "</p>";
    }
    var zone = plan.zones.filter(function (x) { return x.id === sel.id; })[0];
    return '<div class="d-head">' + K.lg(ZONE[zone.id].sym, "zi big") + "<div><b>" + esc(zone.label) + "</b><span>" + esc(zone.tag) + "</span></div></div><p>" + esc(ZONE[zone.id].text) + "</p>";
  }
  function apply() {
    var sel = pinned || cur;
    var on = {}, hotA = {}, hotS = {};
    if (sel) {
      links.forEach(function (l) {
        var hit = sel.type === "agent" ? l.a === sel.id : sel.type === "svc" ? l.s === sel.id : (launchOf[l.a] && launchOf[l.a].zone === sel.id);
        if (hit) { on[l.a + ">" + l.s] = 1; hotA[l.a] = 1; hotS[l.s] = 1; }
      });
      if (sel.type === "agent") {   // fellow agents at its shared services
        sharedWith(sel.id).forEach(function (m) { if (m.at.kind !== "Model backend") hotA[m.who] = hotA[m.who] || 2; });
      }
    }
    root.classList.toggle("has-focus", !!sel);
    Array.prototype.forEach.call(root.querySelectorAll(".agent"), function (el) {
      var n = el.getAttribute("data-pb");
      el.classList.toggle("hot", !!sel && hotA[n] === 1); el.classList.toggle("meet", !!sel && hotA[n] === 2);
      el.classList.toggle("dim", !!sel && !hotA[n]);
      el.setAttribute("aria-pressed", String(!!pinned && pinned.type === "agent" && pinned.id === n));
    });
    Array.prototype.forEach.call(root.querySelectorAll(".svc"), function (el) {
      var id = el.getAttribute("data-svc");
      el.classList.toggle("hot", !!sel && !!hotS[id]); el.classList.toggle("dim", !!sel && !hotS[id]);
      el.setAttribute("aria-pressed", String(!!pinned && pinned.type === "svc" && pinned.id === id));
    });
    Array.prototype.forEach.call(root.querySelectorAll(".zone"), function (el) {
      var id = el.getAttribute("data-zone");
      el.classList.toggle("hot", !!sel && (sel.type === "zone" ? sel.id === id : sel.type === "agent" && launchOf[sel.id] && launchOf[sel.id].zone === id));
    });
    pathEls.forEach(function (el) { el.classList.toggle("on", !!on[el.dataset.a + ">" + el.dataset.s]); });
    detail.innerHTML = describe(sel);
  }
  function setCur(sel) { cur = sel; apply(); }
  function same(a, b) { return a && b && a.type === b.type && a.id === b.id; }
  function selOf(el) {
    var a = el.closest(".agent"), s = el.closest(".svc"), z = el.closest(".zhead");
    return a ? { type: "agent", id: a.getAttribute("data-pb") } : s ? { type: "svc", id: s.getAttribute("data-svc") } : z ? { type: "zone", id: z.getAttribute("data-zone") } : null;
  }
  ["mouseover", "focusin"].forEach(function (ev) {
    root.addEventListener(ev, function (e) {
      if (ev === "mouseover" && e.pointerType === "touch") return;
      var s = selOf(e.target); if (s) setCur(s);
    });
  });
  ["mouseout", "focusout"].forEach(function (ev) {
    root.addEventListener(ev, function (e) {
      if (!root.contains(e.relatedTarget) || !selOf(e.relatedTarget)) setCur(null);
    });
  });
  root.addEventListener("click", function (e) {
    if (e.target.closest(".copy")) return;
    var s = selOf(e.target); if (!s) return;
    pinned = same(pinned, s) ? null : s; apply();
  });
  document.addEventListener("keydown", function (e) { if (e.key === "Escape" && pinned) { pinned = null; apply(); } });
  detail.addEventListener("click", function (e) {
    var b = e.target.closest(".copy"); if (b) K.copyText(b.getAttribute("data-copy"), b);
  });

  apply();
  if ("ResizeObserver" in window) new ResizeObserver(layout).observe(root);
  window.addEventListener("load", layout);
  if (document.fonts && document.fonts.ready) document.fonts.ready.then(layout);
  K.narrow.addEventListener ? K.narrow.addEventListener("change", layout) : null;
  layout();
})();
