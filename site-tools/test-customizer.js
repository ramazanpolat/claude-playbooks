#!/usr/bin/env node
/* Unit checks for the customizer: it must refuse what cpb refuses, and nothing a
   visitor types may reach the output unquoted. Run by verify-snippets.sh. */
"use strict";
const assert = require("assert");
const core = require("../site/customizer-core.js");

let n = 0;
function ok(name, fn) { fn(); n++; }
function sel(over) { const s = core.defaultSelection("daily-driver"); return Object.assign(s, over || {}); }
function refused(s, re) { const r = core.render(s); assert.strictEqual(r.ok, false, "should refuse"); assert.ok(r.errors.join(" ").match(re), "wrong reason: " + r.errors.join(" | ")); }

core.TEMPLATES.forEach(function (t) {
  ok("default " + t.id, function () {
    ["file", "recipe"].forEach(function (mode) {
      const s = core.defaultSelection(t.id); s.mode = mode;
      const a = core.render(s), b = core.render(core.clone(s));
      assert.ok(a.ok, a.errors && a.errors.join("; "));
      assert.strictEqual(a.text, b.text, "rendering is deterministic");
      assert.ok(/\n$/.test(a.text) && a.text.isWellFormed !== false);
      assert.ok(/^[\x00-\x7f]*$/.test(a.text), "ASCII only");
    });
  });
});
ok("names", function () {
  refused(sel({ name: "Bad Name" }), /name is letters/);
  refused(sel({ name: "set" }), /keyword/);
  refused(sel({ name: "1abc" }), /name is letters/);
  refused(sel({ name: "a'; DROP PLAYBOOK x; --" }), /name is letters/);
  assert.ok(core.render(sel({ name: "my_book-2" })).ok);
});
ok("credential-looking variables are refused, like cpb", function () {
  refused(sel({ vars: [{ key: "MY_TOKEN", value: "abc123xyz" }] }), /looks like a credential/);
  refused(sel({ vars: [{ key: "ANTHROPIC_AUTH_TOKEN", value: "sk-ant-x" }] }), /looks like a credential/);
  refused(sel({ vars: [{ key: "DB_PASSWORD", value: "hunter2" }] }), /looks like a credential/);
  refused(sel({ vars: [{ key: "API_KEY", value: "abc" }] }), /looks like a credential/);
  assert.ok(core.render(sel({ vars: [{ key: "MAX_THINKING_TOKENS", value: "8000" }] })).ok, "an integer cannot be a secret");
  refused(sel({ vars: [{ key: "A", value: "has space" }] }), /characters/);
  refused(sel({ vars: [{ key: "A", value: "x'; y" }] }), /characters/);
  refused(sel({ vars: [{ key: "1A", value: "x" }] }), /variable name/);
});
ok("references, never secrets", function () {
  const s = sel(); s.refs.github = "ghp_" + "a".repeat(30);
  refused(s, /reference such as keychain:github-mcp/);
  const t = sel(); t.refs.github = "keychain:my-github";
  const r = core.render(t); assert.ok(r.ok); assert.ok(r.text.includes("FROM 'keychain:my-github'"));
  const u = core.defaultSelection("router-glm"); u.model.tokenRef = "sk-ant-notareference";
  refused(u, /token reference/);
  u.model.tokenRef = "op://Vault/router/token";
  assert.ok(core.render(u).ok);
});
ok("tool rules and model fields cannot break out of their quotes", function () {
  refused(sel({ allow: ["Bash(x)'; DROP PLAYBOOK y; --"] }), /tool rule/);
  refused(sel({ allow: ["a\nb"] }), /tool rule/);
  refused(sel({ allow: ["Bash(gh pr *)"], deny: ["Bash(gh pr *)"] }), /both allowed and denied/);
  const r = core.defaultSelection("router-glm"); r.model.picker[0].label = "x' --";
  refused(r, /label/);
  const b = core.defaultSelection("router-glm"); b.model.baseUrl = "http://evil'.example";
  refused(b, /router URL/);
  const c = core.defaultSelection("router-glm"); c.model.baseUrl = "ftp://x";
  refused(c, /router URL/);
  refused(sel({ model: { kind: "claude", id: "x y" } }), /model id/);
});
ok("a router is an account: a file names it, a recipe never routes", function () {
  const f = core.defaultSelection("router-glm"); f.mode = "file"; f.model.tokenRef = "keychain:router-token";
  const r = core.render(f); assert.ok(r.ok, (r.errors || []).join("; "));
  assert.ok(r.text.includes("CREATE ACCOUNT IF NOT EXISTS router SET provider = 'gateway', base_url = 'http://localhost:4000/v1', auth_token FROM 'keychain:router-token', model = 'glm-5.3';"), r.text);
  assert.ok(r.text.includes("CREATE PLAYBOOK IF NOT EXISTS glm SET login = 'isolated';") && r.text.includes("USE ACCOUNT router"), r.text);
  assert.ok(r.text.includes("-- min-cpb: " + core.ACCOUNT_CPB + "\n"), r.text);
  const p = core.defaultSelection("router-glm"); p.mode = "recipe";
  const t = core.render(p); assert.ok(t.ok);
  assert.ok(!/ANTHROPIC_|USE ACCOUNT|CREATE ACCOUNT IF NOT EXISTS router SET[^\n]*;\n/.test(t.text.split("\n\n")[1]), t.text);
  assert.ok(/^-- needs: an account for your router, such as cpb CREATE ACCOUNT router SET .*auth = 'none'.*; then cpb play router-glm --account router$/m.test(t.text), t.text);
  assert.ok(t.text.includes("SET login = 'isolated';"), "a routed recipe keeps its own login");
  assert.ok(t.commands.indexOf("cpb ALTER PLAYBOOK glm USE ACCOUNT router") > 0, t.commands.join(" | "));
  const n = core.defaultSelection("router-glm"); n.isolated = false; n.mode = "file";
  assert.ok(core.render(n).text.includes("SET login = 'isolated'"), "an account without a Claude login forces an isolated one");
  const d = core.render(core.defaultSelection("daily-driver"));
  assert.ok(d.text.includes("-- min-cpb: " + core.MIN_CPB + "\n") && !/ACCOUNT/.test(d.text));
});
ok("an account's address and name are what cpb takes", function () {
  const h = core.defaultSelection("router-glm"); h.model.baseUrl = "http://llm.example.com/v1";
  refused(h, /router URL/);
  for (const u of ["https://llm.example.com/v1", "http://127.0.0.1:8080/v1", "http://localhost:4000"]) {
    const o = core.defaultSelection("router-glm"); o.model.baseUrl = u; assert.ok(core.render(o).ok, u);
  }
  for (const a of ["machine", "Router", "sk-ant-x", "-x", ""]) {
    const o = core.defaultSelection("router-glm"); o.model.account = a; refused(o, /account name/);
  }
  const g = core.defaultSelection("router-glm"); g.model.account = "glm.local_2"; assert.ok(core.render(g).ok);
});
ok("unknown things are refused", function () {
  refused(sel({ mcp: ["github", "nope"] }), /Unknown MCP/);
  refused(sel({ skills: ["nope"] }), /Unknown skill/);
  refused(sel({ plugins: ["nope"] }), /Unknown plugin/);
});
ok("a keyword cannot name a playbook, a word that only looks like one can", function () {
  for (const w of ["model", "url", "describe", "description"]) {
    refused(sel({ name: w }), /keyword/);
  }
  for (const w of ["alias", "isolated", "login", "select", "launcher"]) {
    assert.ok(core.render(sel({ name: w })).ok, w);
  }
});
ok("sandbox implies an isolated login; flags go where cpb takes them", function () {
  const s = sel({ sandbox: true, isolated: true, mode: "file" });
  const f = core.render(s);
  assert.ok(f.text.includes("CREATE PLAYBOOK IF NOT EXISTS work SET sandbox.always = true, login = 'isolated';"), f.text);
  assert.strictEqual((f.text.match(/login = 'isolated'/g) || []).length, 1, f.text);
  const r = core.render(Object.assign(core.clone(s), { mode: "recipe" }));
  assert.ok(!/CREATE PLAYBOOK/.test(r.text) && !/login = 'isolated'/.test(r.text));
  assert.deepStrictEqual(r.commands[0], "cpb CREATE PLAYBOOK work SET sandbox.always = true, login = 'isolated'");
  assert.ok(r.text.includes("-- create-with: SANDBOX\n"));
  const i = core.render(sel({ isolated: true, mode: "recipe" }));
  assert.ok(i.text.includes("SET login = 'isolated';"));
});
ok("an empty recipe is refused, an empty file is just the playbook", function () {
  const e = sel({ mcp: [], skills: [], plugins: [], allow: [], deny: [], vars: [], model: { kind: "default" } });
  e.mode = "recipe"; refused(e, /at least one thing/);
  e.mode = "file"; const r = core.render(e); assert.ok(r.ok && /CREATE PLAYBOOK IF NOT EXISTS work;/.test(r.text) && !/ALTER/.test(r.text));
});
ok("output order does not depend on the order things were switched on", function () {
  const a = core.render(sel({ mcp: ["linear", "github"], skills: ["xlsx", "pdf"] }));
  const b = core.render(sel({ mcp: ["github", "linear"], skills: ["pdf", "xlsx"] }));
  assert.strictEqual(a.text, b.text);
});
ok("random selections are reproducible and always valid", function () {
  for (let i = 0; i < 300; i++) {
    const t = core.TEMPLATES[i % core.TEMPLATES.length].id;
    const a = core.randomSelection(t, i), b = core.randomSelection(t, i);
    assert.deepStrictEqual(a, b);
    const r = core.render(a);
    assert.ok(r.ok, t + " #" + i + ": " + (r.errors || []).join("; "));
  }
});
console.log("ok    customizer unit checks: " + n + " groups pass");
