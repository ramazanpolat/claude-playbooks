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
ok("unknown things are refused", function () {
  refused(sel({ mcp: ["github", "nope"] }), /Unknown MCP/);
  refused(sel({ skills: ["nope"] }), /Unknown skill/);
  refused(sel({ plugins: ["nope"] }), /Unknown plugin/);
});
ok("sandbox implies an isolated login; flags go where cpb takes them", function () {
  const s = sel({ sandbox: true, isolated: true, noProfile: true, mode: "file" });
  const f = core.render(s);
  assert.ok(f.text.includes("CREATE PLAYBOOK IF NOT EXISTS work SANDBOX NO PILOT PROFILE;"), f.text);
  assert.ok(!/ISOLATED LOGIN/.test(f.text));
  const r = core.render(Object.assign(core.clone(s), { mode: "recipe" }));
  assert.ok(!/CREATE PLAYBOOK/.test(r.text) && !/ISOLATED LOGIN/.test(r.text));
  assert.deepStrictEqual(r.commands[0], "cpb CREATE PLAYBOOK work SANDBOX NO PILOT PROFILE");
  assert.ok(r.text.includes("-- create-with: SANDBOX NO PILOT PROFILE"));
  const i = core.render(sel({ isolated: true, mode: "recipe" }));
  assert.ok(i.text.includes("SET ISOLATED LOGIN;"));
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
ok("the router warning", function () {
  const r = core.defaultSelection("router-glm"); r.noProfile = false;
  assert.strictEqual(core.render(r).warnings.length, 1);
  r.noProfile = true; assert.strictEqual(core.render(r).warnings.length, 0);
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
