/* The template gallery's brain: the catalog of well-known things, the curated
   templates, and the one function that turns a selection into cpb statements.

   It runs in the page (templates.js) and under Node (site-tools/), where CI
   renders every template and a large set of option combinations and applies
   them with a real cpb. So what a visitor copies is what CI checked: the
   page has no second implementation to drift.

   A selection is plain data (see defaultSelection). render() returns the text
   and the commands that use it, or a list of reasons it refuses; it never
   emits something cpb would not accept (names, references, credential-looking
   values). */
(function (root, factory) {
  if (typeof module === "object" && module.exports) module.exports = factory();
  else root.cpbTemplates = factory();
})(this, function () {
  "use strict";

  /* ---------- the catalog: real, public things ---------- */
  var MIN_CPB = "3.24.0";           // the oldest cpb whose grammar these recipes use
  var MARKET = { name: "claude-code-plugins", from: "github:anthropics/claude-code" };
  var SKILLS_SRC = "github:anthropics/skills";

  var MODELS = [
    { id: "claude-opus-5-5", label: "Claude Opus 5.5" },
    { id: "claude-sonnet-5-5", label: "Claude Sonnet 5.5" },
    { id: "claude-haiku-4-5-20251001", label: "Claude Haiku 4.5" }
  ];

  var MCP = [
    { id: "github", label: "GitHub", sym: "b-github", url: "https://api.githubcopilot.com/mcp/", ref: "keychain:github-mcp", blurb: "repos, pull requests, issues" },
    { id: "linear", label: "Linear", sym: "b-linear", url: "https://mcp.linear.app/mcp", blurb: "issues and projects" },
    { id: "notion", label: "Notion", sym: "b-notion", url: "https://mcp.notion.com/mcp", blurb: "pages and databases" },
    { id: "sentry", label: "Sentry", sym: "b-sentry", url: "https://mcp.sentry.dev/mcp", ref: "keychain:sentry-auth", blurb: "errors and traces" },
    { id: "cloudflare-docs", label: "Cloudflare docs", sym: "b-cloudflare", url: "https://docs.mcp.cloudflare.com/mcp", blurb: "the Cloudflare documentation" },
    { id: "playwright", label: "Playwright", sym: "i-mcp", command: "npx", args: ["@playwright/mcp@latest"], blurb: "drive a real browser" },
    { id: "context7", label: "Context7", sym: "i-mcp", command: "npx", args: ["-y", "@upstash/context7-mcp"], blurb: "up-to-date library docs" }
  ];

  var SKILLS = [
    { id: "pdf", label: "pdf", blurb: "read, fill and make PDFs" },
    { id: "docx", label: "docx", blurb: "Word documents" },
    { id: "xlsx", label: "xlsx", blurb: "spreadsheets" },
    { id: "pptx", label: "pptx", blurb: "slide decks" },
    { id: "doc-coauthoring", label: "doc-coauthoring", blurb: "write long documents together" },
    { id: "internal-comms", label: "internal-comms", blurb: "updates, newsletters, FAQs" },
    { id: "frontend-design", label: "frontend-design", blurb: "distinctive front-end interfaces" },
    { id: "web-artifacts-builder", label: "web-artifacts-builder", blurb: "multi-part HTML artifacts" },
    { id: "webapp-testing", label: "webapp-testing", blurb: "test web apps with Playwright" },
    { id: "theme-factory", label: "theme-factory", blurb: "themes for artifacts" },
    { id: "canvas-design", label: "canvas-design", blurb: "posters and visual design" },
    { id: "algorithmic-art", label: "algorithmic-art", blurb: "generative art" },
    { id: "slack-gif-creator", label: "slack-gif-creator", blurb: "GIFs for Slack" },
    { id: "mcp-builder", label: "mcp-builder", blurb: "build MCP servers" },
    { id: "skill-creator", label: "skill-creator", blurb: "write new skills" }
  ];

  var PLUGINS = [
    { id: "commit-commands", blurb: "commit, push and PR commands" },
    { id: "feature-dev", blurb: "a guided feature workflow" },
    { id: "frontend-design", blurb: "front-end design guidance" },
    { id: "code-review", blurb: "automated pull-request review" },
    { id: "pr-review-toolkit", blurb: "specialised review agents" },
    { id: "security-guidance", blurb: "security reminders while you edit" },
    { id: "hookify", blurb: "hooks from your conversations" },
    { id: "explanatory-output-style", blurb: "explains as it goes" },
    { id: "learning-output-style", blurb: "learn by doing" }
  ];

  var RULES = {
    allow: ["Bash(gh pr *)", "Bash(gh pr view *)", "Bash(git diff *)", "Bash(git status)", "Bash(kubectl get *)", "Bash(kubectl describe *)", "Bash(kubectl logs *)", "Bash(npm test *)"],
    deny: ["Bash(git push --force *)", "Bash(rm -rf *)", "Bash(kubectl delete *)", "Bash(kubectl apply *)", "Bash(curl *)", "Edit", "Write"]
  };

  var VARS = [
    { key: "MAX_THINKING_TOKENS", value: "8000", blurb: "a bigger thinking budget" },
    { key: "DISABLE_TELEMETRY", value: "1", blurb: "no telemetry" }
  ];

  /* ---------- the curated templates ---------- */
  function T(o) {
    var d = { model: { kind: "default", id: "" }, mcp: [], skills: [], plugins: [], allow: [], deny: [], vars: [],
      sandbox: false, isolated: false, noProfile: false };
    Object.keys(o.defaults || {}).forEach(function (k) { d[k] = o.defaults[k]; });
    o.defaults = d;
    return o;
  }
  var TEMPLATES = [
    T({ id: "daily-driver", color: 0, name: "work", glyph: "g-work", title: "Daily driver",
      tagline: "Your everyday Claude Code.",
      description: "The everyday setup: GitHub and Linear, the document skills, Opus, and a guard against force pushes.",
      defaults: { model: { kind: "claude", id: "claude-opus-5-5" }, mcp: ["github", "linear"], skills: ["pdf", "docx", "xlsx", "pptx"],
        plugins: ["commit-commands", "feature-dev"], allow: ["Bash(gh pr *)"], deny: ["Bash(git push --force *)"],
        vars: [{ key: "MAX_THINKING_TOKENS", value: "8000" }] } }),
    T({ id: "code-reviewer", color: 4, name: "reviewer", glyph: "g-reviewer", title: "Code reviewer",
      tagline: "Reads code. Never writes it.",
      description: "Reads code and never writes it: the review plugins, GitHub, and Edit and Write denied.",
      defaults: { model: { kind: "claude", id: "claude-sonnet-5-5" }, mcp: ["github"], plugins: ["code-review", "pr-review-toolkit"],
        allow: ["Bash(gh pr view *)", "Bash(git diff *)"], deny: ["Edit", "Write"] } }),
    T({ id: "sre-sandbox", color: 2, name: "sre", glyph: "g-sre", title: "SRE in a sandbox",
      tagline: "Operations work, inside a microVM.",
      description: "For operations work in a microVM: the Cloudflare docs, runbooks with doc-coauthoring, and read-only kubectl.",
      defaults: { mcp: ["cloudflare-docs"], skills: ["doc-coauthoring"],
        allow: ["Bash(kubectl get *)", "Bash(kubectl describe *)", "Bash(kubectl logs *)"],
        deny: ["Bash(kubectl delete *)", "Bash(kubectl apply *)"], sandbox: true } }),
    T({ id: "second-account", color: 1, name: "side", glyph: "g-side", title: "Second account",
      tagline: "Another Anthropic account, side by side.",
      description: "A second Anthropic account beside your first: an isolated login, Notion, Playwright and the creative skills.",
      defaults: { model: { kind: "claude", id: "claude-sonnet-5-5" }, mcp: ["notion", "playwright"], skills: ["canvas-design", "algorithmic-art"],
        plugins: ["frontend-design"], vars: [{ key: "DISABLE_TELEMETRY", value: "1" }], isolated: true } }),
    T({ id: "router-glm", color: 3, name: "glm", glyph: "g-glm", title: "Another model backend",
      tagline: "Send one playbook to a router.",
      description: "A playbook for a model backend behind your router (GLM here): its own login, the API key blocked, a /model list of its own.",
      defaults: { model: { kind: "router", baseUrl: "http://localhost:4000/v1", id: "glm-5.3", blockKey: true, tokenRef: "",
          picker: [{ id: "glm-5.3", label: "GLM 5.3" }, { id: "glm-5.3-flash", label: "GLM 5.3 Flash", behavesAs: "claude-sonnet-5" }], pickerMode: "ONLY" },
        mcp: ["context7"], isolated: true, noProfile: true } }),
    T({ id: "docs-writer", color: 0, name: "docs", glyph: "g-docs", title: "Documents and writing",
      tagline: "Word, PDF, sheets and slides.",
      description: "For documents and writing: Word, PDF, spreadsheets and slides, co-authoring, and Notion.",
      defaults: { model: { kind: "claude", id: "claude-sonnet-5-5" }, mcp: ["notion"],
        skills: ["pdf", "docx", "xlsx", "pptx", "doc-coauthoring", "internal-comms"] } }),
    T({ id: "frontend-craft", color: 4, name: "ui", glyph: "g-ui", title: "Front-end craft",
      tagline: "Build it, then watch it run.",
      description: "For front-end work: the design plugin and skills, Playwright for the browser, Context7 for library docs.",
      defaults: { model: { kind: "claude", id: "claude-sonnet-5-5" }, mcp: ["playwright", "context7"],
        skills: ["frontend-design", "web-artifacts-builder", "webapp-testing", "theme-factory"], plugins: ["frontend-design"],
        allow: ["Bash(npm test *)"] } })
  ];

  function clone(x) { return JSON.parse(JSON.stringify(x)); }
  function byId(list, id) { for (var i = 0; i < list.length; i++) if (list[i].id === id) return list[i]; return null; }
  function template(id) { return byId(TEMPLATES, id); }

  function defaultSelection(id) {
    var t = template(id);
    if (!t) throw new Error("no template " + id);
    var s = clone(t.defaults);
    s.template = id;
    s.mode = "file";
    s.name = t.name;
    s.refs = {};                                    // mcp id -> reference override
    MCP.forEach(function (m) { if (m.ref) s.refs[m.id] = m.ref; });
    return s;
  }

  /* ---------- validation: the rules cpb itself enforces ---------- */
  var KEYWORDS = ("ADD ALIAS ALTER APPLY AS BEHAVES BLOCK BRANCH CREATE DEFAULTS DESCRIBE DROP ENV ENVS EXISTS EXPLAIN FROM IF INCLUDE " +
    "ISOLATED LINK LOGIN NO NOT OR PLAYBOOK PLAYBOOKS REPLACE RENAME SANDBOX SELECT SET SHOW SUBDIR TO UNSET USE VAR").split(" ");
  var NAME = /^[a-z][a-z0-9_-]{0,39}$/;
  var KEY = /^[A-Za-z_][A-Za-z0-9_]{0,63}$/;
  var VALUE = /^[A-Za-z0-9_.:\/@%+=,~-]{0,200}$/;
  var REF = /^(?:[a-z][a-z0-9]*:[A-Za-z0-9_.\/:#@+~=-]+)$/;
  var URLRE = /^https?:\/\/[A-Za-z0-9.-]+(?::\d{1,5})?(?:\/[A-Za-z0-9._~\/%+-]*)?$/;
  var MODELID = /^[A-Za-z0-9._:\/@-]{1,80}$/;
  var LABEL = /^[A-Za-z0-9][A-Za-z0-9 ._()\/-]{0,39}$/;
  var RULE = /^[^'\n\r;]{1,100}$/;
  // keys cpb treats as credentials: a literal is refused unless it cannot be a secret
  var CRED = /TOKEN|SECRET|PASSWORD|PASSWD|AUTH|CREDENTIAL|APIKEY|API_KEY|(^|_)KEY$/i;
  function secretLike(key, value) {
    return CRED.test(key) && !(value === "" || /^\d+$/.test(value) || value === "true" || value === "false");
  }

  /* one message per field, or "" when it is fine: the page marks the field with it */
  var check = {
    name: function (v) {
      if (!NAME.test(v || "")) return "Letters, digits, _ and -, starting with a letter.";
      if (KEYWORDS.indexOf(v.toUpperCase()) >= 0) return "\u201c" + v + "\u201d is a cpb keyword; pick another name.";
      return "";
    },
    ref: function (v, example) { return REF.test(v || "") ? "" : "A reference such as " + (example || "keychain:name") + ", never the secret itself."; },
    url: function (v) { return URLRE.test(v || "") ? "" : "An http(s) URL, such as http://localhost:4000/v1."; },
    modelId: function (v) { return MODELID.test(v || "") ? "" : "Letters, digits and . _ : / @ -."; },
    label: function (v) { return !v || LABEL.test(v) ? "" : "Letters, digits, spaces and . _ ( ) / -."; },
    rule: function (v) { return RULE.test(v || "") ? "" : "No quotes, semicolons or line breaks."; },
    varName: function (k, v) {
      if (!KEY.test(k || "")) return "Letters, digits and _, not starting with a digit.";
      if (!VALUE.test(v || "")) return "A plain value: no spaces or quotes.";
      if (secretLike(k, v || "")) return k + " looks like a credential, and cpb keeps those out of files. Use a reference instead.";
      return "";
    }
  };

  function validate(sel) {
    var errs = [], t = template(sel.template);
    if (!t) return ["unknown template"];
    if (!NAME.test(sel.name || "")) errs.push("The name is letters, digits, _ and -, and starts with a letter.");
    else if (KEYWORDS.indexOf(sel.name.toUpperCase()) >= 0) errs.push("“" + sel.name + "” is a cpb keyword; pick another name.");
    var m = sel.model || { kind: "default" };
    if (m.kind === "claude" && !MODELID.test(m.id || "")) errs.push("The model id is not valid.");
    if (m.kind === "router") {
      if (!URLRE.test(m.baseUrl || "")) errs.push("The router URL must be an http(s) URL.");
      if (!MODELID.test(m.id || "")) errs.push("The routed model id is not valid.");
      if (m.tokenRef && !REF.test(m.tokenRef)) errs.push("The token reference looks like keychain:name (a scheme and a name), never the token itself.");
      (m.picker || []).forEach(function (e) {
        if (!MODELID.test(e.id || "")) errs.push("A /model entry has an invalid id.");
        if (e.label && !LABEL.test(e.label)) errs.push("A /model label has characters cpb cannot take.");
        if (e.behavesAs && !MODELID.test(e.behavesAs)) errs.push("A “behaves as” id is invalid.");
      });
      if ((m.picker || []).length && m.pickerMode !== "ONLY" && m.pickerMode !== "APPEND") errs.push("The /model list is ONLY or APPEND.");
    }
    var refs = sel.refs || {};
    sel.mcp.forEach(function (id) {
      var def = byId(MCP, id);
      if (!def) { errs.push("Unknown MCP server " + id + "."); return; }
      if (def.ref && !REF.test(refs[id] || "")) errs.push("The " + def.label + " credential is a reference such as " + def.ref + ", never the secret itself.");
    });
    sel.skills.forEach(function (id) { if (!byId(SKILLS, id)) errs.push("Unknown skill " + id + "."); });
    sel.plugins.forEach(function (id) { if (!byId(PLUGINS, id)) errs.push("Unknown plugin " + id + "."); });
    sel.allow.concat(sel.deny).forEach(function (r) { if (!RULE.test(r)) errs.push("A tool rule has characters cpb rules cannot take: " + r.slice(0, 30)); });
    sel.allow.forEach(function (r) { if (sel.deny.indexOf(r) >= 0) errs.push("“" + r + "” is both allowed and denied."); });
    sel.vars.forEach(function (v) {
      if (!KEY.test(v.key || "")) errs.push("A variable name is letters, digits and _.");
      else if (!VALUE.test(v.value || "")) errs.push(v.key + ": the value has characters a plain value cannot take.");
      else if (secretLike(v.key, v.value || "")) errs.push(v.key + " looks like a credential. cpb keeps those out of files: use a reference instead.");
    });
    return errs;
  }

  /* ---------- rendering ---------- */
  function q(s) { return "'" + s + "'"; }
  function ordered(list, ids) { return list.filter(function (x) { return ids.indexOf(x.id) >= 0; }); }

  function clauses(sel) {
    var out = [];
    var plugins = ordered(PLUGINS, sel.plugins);
    if (plugins.length) {
      out.push("ADD MARKETPLACE " + MARKET.name + " FROM " + q(MARKET.from));
      plugins.forEach(function (p) { out.push("ADD PLUGIN " + p.id + "@" + MARKET.name); });
    }
    ordered(MCP, sel.mcp).forEach(function (m) {
      var c = "ADD MCP SERVER " + m.id + " ";
      if (m.url) {
        c += "URL " + q(m.url);
        if (m.ref) c += " HEADER 'Authorization' FROM " + q(sel.refs[m.id]);
      } else c += "COMMAND " + q(m.command) + " ARGS " + m.args.map(q).join(" ");
      out.push(c);
    });
    ordered(SKILLS, sel.skills).forEach(function (s) { out.push("ADD SKILL " + s.id + " FROM " + q(SKILLS_SRC) + " SUBDIR " + q("skills/" + s.id)); });
    var order = function (rules, preset) { return preset.filter(function (r) { return rules.indexOf(r) >= 0; }).concat(rules.filter(function (r) { return preset.indexOf(r) < 0; })); };
    if (sel.allow.length) out.push("ALLOW TOOL " + order(sel.allow, RULES.allow).map(q).join(" "));
    if (sel.deny.length) out.push("DENY TOOL " + order(sel.deny, RULES.deny).map(q).join(" "));
    var m = sel.model || { kind: "default" };
    if (m.kind === "claude" && m.id) out.push("SET MODEL " + q(m.id));
    if (m.kind === "router") {
      out.push("SET VAR ANTHROPIC_BASE_URL=" + m.baseUrl);
      out.push("SET VAR ANTHROPIC_MODEL=" + m.id);
      if (m.tokenRef) out.push("SET VAR ANTHROPIC_AUTH_TOKEN FROM " + q(m.tokenRef));
      if (m.blockKey) out.push("BLOCK VAR ANTHROPIC_API_KEY");
      (m.picker || []).forEach(function (e) {
        out.push("ADD MODEL " + q(e.id) + (e.label ? " LABEL " + q(e.label) : "") + (e.behavesAs ? " BEHAVES AS " + q(e.behavesAs) : ""));
      });
      if ((m.picker || []).length) out.push("SET MODEL PICKER " + m.pickerMode);
    }
    sel.vars.forEach(function (v) { out.push("SET VAR " + v.key + "=" + (v.value || "")); });
    if (sel.mode === "recipe" && sel.isolated && !sel.sandbox) out.push("SET ISOLATED LOGIN");
    return out;
  }

  function createFlags(sel) {
    var f = [];
    if (sel.sandbox) f.push("SANDBOX");
    if (sel.mode === "file" && sel.isolated && !sel.sandbox) f.push("ISOLATED LOGIN");
    if (sel.noProfile) f.push("NO PILOT PROFILE");
    return f;
  }
  function needs(sel) {
    var refs = [];
    ordered(MCP, sel.mcp).forEach(function (m) { if (m.ref) refs.push(sel.refs[m.id]); });
    var m = sel.model || {};
    if (m.kind === "router" && m.tokenRef) refs.push(m.tokenRef);
    return refs;
  }
  function header(sel) {
    var t = template(sel.template), l = [];
    l.push("-- title: " + t.title);
    l.push("-- description: " + t.description);
    l.push("-- min-cpb: " + MIN_CPB);
    var n = needs(sel), parts = [];
    if (n.length) parts.push("a secret helper for " + n.join(", "));
    if (sel.model && sel.model.kind === "router") parts.push("your router's key, attached with an env set (it is not stored in this file)");
    if (parts.length) l.push("-- needs: " + parts.join("; "));
    if (sel.mode === "recipe") {
      if (sel.sandbox) l.push("-- create-with: SANDBOX");
    }
    return l;
  }

  function render(sel) {
    var errs = validate(sel);
    if (errs.length) return { ok: false, errors: errs };
    var body = clauses(sel), lines = header(sel), name = sel.name, flags = createFlags(sel);
    lines.push("");
    if (sel.mode === "file") {
      lines.push("CREATE PLAYBOOK IF NOT EXISTS " + name + (flags.length ? " " + flags.join(" ") : "") + ";");
      if (body.length) lines.push("ALTER PLAYBOOK " + name + "\n  " + body.join("\n  ") + ";");
    } else {
      if (!body.length) return { ok: false, errors: ["A recipe needs at least one thing to apply."], empty: true };
      lines.push("ALTER PLAYBOOK\n  " + body.join("\n  ") + ";");
    }
    var text = lines.join("\n") + "\n";
    var file = (sel.mode === "file" ? name : sel.template) + ".cpb";
    var cmds = [];
    if (sel.mode === "file") {
      cmds.push("cpb APPLY " + file + " --dry-run", "cpb APPLY " + file, name);
    } else {
      var cw = []; if (sel.sandbox) cw.push("SANDBOX"); if (sel.noProfile) cw.push("NO PILOT PROFILE");
      if (cw.length) cmds.push("cpb CREATE PLAYBOOK " + name + " " + cw.join(" "));
      cmds.push("cpb APPLY " + file + " TO " + name + " --dry-run", "cpb APPLY " + file + " TO " + name, name);
    }
    var warns = [];
    if (sel.model && sel.model.kind === "router" && !sel.noProfile) {
      warns.push("This playbook is routed away from Anthropic and still imports ~/.pilot-profile/. cpb will warn once; turn on NO PILOT PROFILE to keep that profile out of its CLAUDE.md.");
    }
    return { ok: true, text: text, file: file, create: flags, commands: cmds, needs: needs(sel), warnings: warns, lines: body.length };
  }

  function canonical(id) {
    var s = defaultSelection(id);
    s.mode = "recipe";
    return render(s);
  }

  /* ---------- for CI: many valid selections, reproducibly ---------- */
  function rng(seed) {
    var a = seed >>> 0;
    return function () {
      a = (a + 0x6D2B79F5) >>> 0;
      var t = a;
      t = Math.imul(t ^ (t >>> 15), t | 1);
      t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
      return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
    };
  }
  function pick(rand, list, p) { return list.filter(function () { return rand() < p; }); }
  function randomSelection(id, seed) {
    var r = rng(seed), s = defaultSelection(id);
    s.mode = r() < 0.5 ? "file" : "recipe";
    s.name = "n" + seed;
    s.sandbox = r() < 0.4; s.isolated = r() < 0.4; s.noProfile = r() < 0.4;
    s.mcp = pick(r, MCP, 0.45).map(function (m) { return m.id; });
    s.skills = pick(r, SKILLS, 0.35).map(function (x) { return x.id; });
    s.plugins = pick(r, PLUGINS, 0.35).map(function (x) { return x.id; });
    s.deny = pick(r, RULES.deny, 0.35);
    s.allow = pick(r, RULES.allow, 0.4).filter(function (x) { return s.deny.indexOf(x) < 0; });
    s.vars = pick(r, VARS, 0.5).map(function (v) { return { key: v.key, value: v.value }; });
    var k = r();
    if (k < 0.3) s.model = { kind: "default", id: "" };
    else if (k < 0.7) s.model = { kind: "claude", id: MODELS[Math.floor(r() * MODELS.length)].id };
    else s.model = { kind: "router", baseUrl: "http://localhost:" + (4000 + Math.floor(r() * 100)) + "/v1", id: "glm-5.3", blockKey: r() < 0.7,
      tokenRef: r() < 0.5 ? "keychain:router-token" : "", pickerMode: r() < 0.5 ? "ONLY" : "APPEND",
      picker: r() < 0.7 ? [{ id: "glm-5.3", label: "GLM 5.3" }, { id: "glm-5.3-flash", label: "GLM 5.3 Flash", behavesAs: "claude-sonnet-5" }] : [] };
    if (s.mode === "recipe" && !clauses(s).length) s.skills = ["pdf"];
    return s;
  }
  function everything(id, mode) {
    var s = defaultSelection(id);
    s.mode = mode;
    s.name = "all" + mode;
    s.sandbox = true; s.noProfile = true; s.isolated = true;
    s.mcp = MCP.map(function (m) { return m.id; });
    s.skills = SKILLS.map(function (x) { return x.id; });
    s.plugins = PLUGINS.map(function (x) { return x.id; });
    s.allow = RULES.allow.slice(); s.deny = RULES.deny.filter(function (x) { return s.allow.indexOf(x) < 0; });
    s.vars = VARS.map(function (v) { return { key: v.key, value: v.value }; });
    s.model = clone(defaultSelection("router-glm").model);
    s.model.tokenRef = "keychain:router-token";
    return s;
  }

  return {
    MIN_CPB: MIN_CPB, MARKET: MARKET, SKILLS_SRC: SKILLS_SRC, MODELS: MODELS, MCP: MCP, SKILLS: SKILLS, PLUGINS: PLUGINS, RULES: RULES, VARS: VARS,
    TEMPLATES: TEMPLATES, template: template, defaultSelection: defaultSelection, validate: validate, render: render,
    canonical: canonical, randomSelection: randomSelection, everything: everything,
    secretLike: secretLike, check: check, clone: clone
  };
});
