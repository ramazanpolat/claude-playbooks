(function () {
  "use strict";

  /* The headline: two reels that spin and land on a combination cpb can really
     do. Every pair is a promise the page keeps, so the reels never mix words
     that were not written together; the filler that flashes by while they spin
     is only a blur. */
  var h1 = document.getElementById("slot");
  if (!h1) return;
  var reduce = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
  var reels = { verb: h1.querySelector('[data-reel="verb"]'), obj: h1.querySelector('[data-reel="obj"]') };
  var sub = document.getElementById("slot-sub");
  var lever = document.getElementById("lever");

  var PAIRS = [
    { v: "BRING", o: "CHARACTER", c: 0, href: "#playbooks", link: "See the notebooks",
      t: "Each playbook has its own <code class=\"inl\">CLAUDE.md</code>, skills, plugins and memory, so each Claude has its own personality and know-how." },
    { v: "ISOLATE", o: "SESSIONS", c: 4, href: "#layers", link: "See the layers",
      t: "A config directory per playbook: history, memory and sessions never mix. Your <code class=\"inl\">~/.claude</code> never moves." },
    { v: "SPLIT", o: "LOGINS", c: 1, href: "#layers", link: "See the layers",
      t: "Run two Anthropic accounts side by side. An isolated login shares nothing with the other." },
    { v: "SWAP", o: "MODELS", c: 3, href: "#playbooks", link: "See the glm notebook",
      t: "Point one playbook at another model backend with an env set, and give it its own <code class=\"inl\">/model</code> list. Your shell stays as it is." },
    { v: "SANDBOX", o: "AGENTS", c: 2, href: "#runtimes", link: "See where they run",
      t: "A microVM sees only your folder and the playbook's own directory. API keys stay on the host." },
    { v: "CONNECT", o: "TOOLS", c: 0, href: "#playbooks", link: "See the MCP servers",
      t: "MCP servers per playbook, with credentials by reference, never by value." },
    { v: "ENHANCE", o: "COORDINATION", c: 4, href: "#runtimes", link: "See what they share",
      t: "Isolated by default, connected on purpose: agents meet at a shared MCP server, folder or router, and nowhere else." },
    { v: "REPLAY", o: "SETUPS", c: 1, href: "#recipes", link: "See the recipe",
      t: "Describe a setup once in a <code class=\"inl\">.cpb</code> file and apply it on any machine. Applying it twice changes nothing." },
    { v: "THROW AWAY", o: "EXPERIMENTS", c: 5, href: "#playbooks", link: "Meet the ghost",
      t: "<code class=\"inl\">cpb start /tmp/x --delete</code>: a throwaway session in a throwaway folder." },
    { v: "LIMIT", o: "POWERS", c: 2, href: "#playbooks", link: "See the reviewer",
      t: "Allow and deny tools per playbook. The reviewer reads code and never writes it." }
  ];
  var VERBS = PAIRS.map(function (p) { return p.v; });
  var OBJS = PAIRS.map(function (p) { return p.o; });
  var longest = function (a) { return a.reduce(function (m, w) { return Math.max(m, w.length); }, 0); };
  reels.verb.querySelector(".win").style.width = longest(VERBS) + "ch";
  reels.obj.querySelector(".win").style.width = longest(OBJS) + "ch";
  var nEl = document.getElementById("lever-n");
  if (nEl) nEl.textContent = PAIRS.length + " combinations";

  var LINE = 1.3;               // em, matches .reel .w
  var current = 0, busy = false, touched = false, order = [0], pos = 0, timer = null;

  function shuffled() {
    var rest = PAIRS.map(function (_, i) { return i; }).filter(function (i) { return i !== current; });
    for (var i = rest.length - 1; i > 0; i--) { var j = Math.floor(Math.random() * (i + 1)); var t = rest[i]; rest[i] = rest[j]; rest[j] = t; }
    return [current].concat(rest);
  }
  order = shuffled();

  function word(w, cls) { return '<span class="w' + (cls ? " " + cls : "") + '">' + w + "</span>"; }
  function colour(i) { h1.className = "slot-h1 c" + PAIRS[i].c; if (lever) lever.className = lever.className.replace(/\bc\d\b/g, "").trim() + " c" + PAIRS[i].c; }

  function setSub(i, animate) {
    var p = PAIRS[i];
    sub.setAttribute("aria-live", touched ? "polite" : "off");
    sub.innerHTML = p.t + ' <a href="' + p.href + '">' + p.link + " &darr;</a>";
    if (animate) { sub.classList.remove("in"); void sub.offsetWidth; sub.classList.add("in"); }
  }

  function settle(reel, w) {
    var strip = reel.querySelector(".strip");
    strip.style.transition = "none";
    strip.style.transform = "none";
    strip.innerHTML = word(w, reduce ? "" : "land");
    reel.classList.remove("spinning");
    reel.classList.add("lit");
  }

  function spinReel(key, from, to, ms) {
    var reel = reels[key], strip = reel.querySelector(".strip");
    var pool = key === "verb" ? VERBS : OBJS;
    var n = 14 + Math.floor(ms / 140);
    var html = word(from);
    var last = from;
    for (var k = 0; k < n; k++) {
      var w;
      do { w = pool[Math.floor(Math.random() * pool.length)]; } while (w === last || w === to);
      html += word(w); last = w;
    }
    html += word(to);
    reel.classList.remove("lit");
    reel.classList.add("spinning");
    strip.style.transition = "none";
    strip.style.transform = "translateY(0)";
    strip.innerHTML = html;
    void strip.offsetHeight;
    strip.style.transition = "transform " + ms + "ms cubic-bezier(0.1, 0.72, 0.18, 1)";
    strip.style.transform = "translateY(" + (-(n + 1) * LINE) + "em)";
    setTimeout(function () { reel.classList.remove("spinning"); }, ms * 0.78);
    return new Promise(function (resolve) { setTimeout(function () { settle(reel, to); resolve(); }, ms + 40); });
  }

  function show(i, animate) {
    if (busy) return Promise.resolve();
    var p = PAIRS[i], from = PAIRS[current];
    busy = true;
    colour(i);
    if (!animate || reduce) {
      settle(reels.verb, p.v); settle(reels.obj, p.o);
      current = i; setSub(i, !reduce); busy = false;
      return Promise.resolve();
    }
    return Promise.all([spinReel("verb", from.v, p.v, 1500), spinReel("obj", from.o, p.o, 2100)]).then(function () {
      current = i; setSub(i, true); busy = false;
    });
  }

  function next() {
    pos = (pos + 1) % order.length;
    if (pos === 0) { order = shuffled(); pos = 1; }
    return show(order[pos], true);
  }

  function pull() {
    if (busy) return;
    touched = true; stopAuto();
    if (!reduce) { lever.classList.add("pulled"); setTimeout(function () { lever.classList.remove("pulled"); }, 260); }
    next();
  }
  if (lever) lever.addEventListener("click", pull);
  h1.querySelector(".slot").addEventListener("click", pull);

  function stopAuto() { if (timer) { clearInterval(timer); timer = null; } }
  function startAuto() {
    if (reduce || touched || timer) return;
    timer = setInterval(function () { if (!document.hidden && !busy) next(); }, 7200);
  }

  /* start on the first pair; spin into it once so the effect is the first thing seen */
  colour(0);
  reels.verb.classList.add("lit"); reels.obj.classList.add("lit");
  setSub(0, false);
  if (!reduce) {
    current = PAIRS.length - 1;            // spin from somewhere else, land on pair 0
    settle(reels.verb, PAIRS[current].v); settle(reels.obj, PAIRS[current].o);
    setTimeout(function () { show(0, true).then(function () { order = shuffled(); pos = 0; }); }, 500);
    setTimeout(startAuto, 4500);
  }
})();
