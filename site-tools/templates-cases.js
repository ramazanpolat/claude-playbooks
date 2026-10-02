#!/usr/bin/env node
/* Renders the curated templates and many customizer selections with the same
   code the page runs (site/customizer-core.js), for CI to apply with a real cpb.

     templates-cases.js --write            write site/p/<id>.cpb and site/p/index.txt
     templates-cases.js --check            fail if site/p/ differs from what the code renders
     templates-cases.js --cases [--random N]   print the cases to apply, as JSON
*/
"use strict";
const fs = require("fs");
const path = require("path");
const core = require("../site/customizer-core.js");

const DIR = path.join(__dirname, "..", "site", "p");

function files() {
  const out = {};
  core.TEMPLATES.forEach(function (t) {
    const r = core.canonical(t.id);
    if (!r.ok) throw new Error(t.id + ": " + r.errors.join("; "));
    out[t.id + ".cpb"] = r.text;
  });
  out["index.txt"] = core.TEMPLATES.map(function (t) { return t.id; }).sort().join("\n") + "\n";
  return out;
}

const arg = process.argv[2];
if (arg === "--write") {
  fs.mkdirSync(DIR, { recursive: true });
  const f = files();
  fs.readdirSync(DIR).forEach(function (n) { if (!(n in f)) fs.unlinkSync(path.join(DIR, n)); });
  Object.keys(f).forEach(function (n) { fs.writeFileSync(path.join(DIR, n), f[n]); });
  console.log("wrote " + Object.keys(f).length + " files into site/p/");
} else if (arg === "--check") {
  const f = files();
  const have = fs.existsSync(DIR) ? fs.readdirSync(DIR) : [];
  let bad = 0;
  Object.keys(f).forEach(function (n) {
    const p = path.join(DIR, n);
    if (!fs.existsSync(p) || fs.readFileSync(p, "utf8") !== f[n]) { console.error("FAIL  site/p/" + n + " differs from what customizer-core.js renders"); bad = 1; }
  });
  have.forEach(function (n) { if (!(n in f)) { console.error("FAIL  site/p/" + n + " is not a template"); bad = 1; } });
  if (bad) { console.error("      regenerate with: node site-tools/templates-cases.js --write"); process.exit(1); }
  console.log("ok    templates: site/p/ has the " + core.TEMPLATES.length + " templates, byte for byte");
} else if (arg === "--cases") {
  const i = process.argv.indexOf("--random");
  const n = i > 0 ? parseInt(process.argv[i + 1], 10) : 6;
  const cases = [];
  function add(kind, sel) {
    const r = core.render(sel);
    if (!r.ok) throw new Error(kind + " " + sel.template + ": " + r.errors.join("; "));
    cases.push({ id: kind + "/" + sel.template + "/" + sel.mode, kind: kind, template: sel.template, mode: sel.mode, name: sel.name,
      file: r.file, text: r.text, create: r.create, needs: r.needs });
  }
  core.TEMPLATES.forEach(function (t) {
    ["file", "recipe"].forEach(function (mode) {
      const d = core.defaultSelection(t.id); d.mode = mode; if (mode === "recipe") d.name = t.name + "r"; add("default", d);
      const e = core.everything(t.id, mode); e.template = t.id; add("everything", e);
    });
    for (let k = 0; k < n; k++) add("random", core.randomSelection(t.id, 1000 * (core.TEMPLATES.indexOf(t) + 1) + k));
  });
  process.stdout.write(JSON.stringify(cases));
} else {
  console.error("usage: templates-cases.js --write | --check | --cases [--random N]");
  process.exit(2);
}
