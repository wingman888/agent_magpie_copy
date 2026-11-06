// dial — one state object per view, rendered into a list. No framework.
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);
const params = new URLSearchParams(location.search);
const mode = params.get("mode") || "window";
document.body.classList.add(mode);
if (params.get("theme")) document.documentElement.dataset.theme = params.get("theme");

let state = { agents: [], profiles: [], catalog: "" };
let providers = null; // { providers, presets, gateway }
let view = "agents";
let period = "30d"; // usage window
let usage = null;   // last usage summary
let pick = null; // { agent, field, options, items, cursor, anchor }
let editing = null; // provider id being edited; { preset } or { custom: true } for a new one
let draft = null; // the editor's working copy
let adding = false; // the preset sheet is open
let activity = false; // recent gateway calls are shown

async function api(path, body) {
  const res = await fetch("/api/" + path, {
    method: body === undefined ? "GET" : "POST",
    headers: { "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (res.status === 204) return null;
  const data = await res.json();
  if (!res.ok) throw new Error(data.error || res.statusText);
  return data;
}

function el(tag, cls, text) {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}
function svg(d, size = 12, stroke = 1.6) {
  const s = document.createElementNS("http://www.w3.org/2000/svg", "svg");
  s.setAttribute("viewBox", "0 0 16 16");
  s.setAttribute("width", size);
  s.setAttribute("height", size);
  s.innerHTML = `<path d="${d}" fill="none" stroke="currentColor" stroke-width="${stroke}" stroke-linecap="round" stroke-linejoin="round"/>`;
  return s;
}
const CHEV = "m5.5 6.5 2.5 2.5 2.5-2.5";
const CHEV_R = "m6.5 4.5 3 3.5-3 3.5";
const CHECK = "m3.5 8.5 3 3 6-7";
const PLUS = "M8 3.5v9M3.5 8h9";

// A brand icon: colour logos are images, mono logos take the text colour.
// Nothing is ever invented: a model with no known vendor keeps the slot
// empty, and a custom provider shows a plain outline ("generic").
function icon(name) {
  const e = el("span", "ic");
  if (name === "generic") {
    e.classList.add("generic");
    e.append(svg("M8 2.2 13.2 5.1v5.8L8 13.8 2.8 10.9V5.1Z M8 8v5.8 M2.8 5.1 8 8l5.2-2.9", 16, 1.4));
    return e;
  }
  if (name) {
    if (name.endsWith("-color") || name === "crush") {
      const img = el("img");
      img.src = `icons/${name}.${name === "crush" ? "png" : "svg"}`;
      img.alt = "";
      img.draggable = false;
      e.append(img);
    } else {
      const m = el("span", "mask");
      m.style.setProperty("--i", `url(icons/${name}.svg)`);
      e.append(m);
    }
    return e;
  }
  e.classList.add("blank");
  return e;
}

function status(msg, kind = "", ms = 3500) {
  const s = $("#status");
  s.textContent = msg;
  s.title = msg;
  s.className = "status " + kind;
  clearTimeout(status.t);
  if (msg) status.t = setTimeout(() => { s.textContent = ""; s.className = "status"; }, ms);
}

// ---------- agents view ----------

function optionFor(field, value) {
  return field.options.find((o) => o.value === value);
}

function renderAgents() {
  const list = $("#agents");
  list.replaceChildren();
  if (!state.agents.length) {
    const e = el("div", "empty-state");
    e.append(el("b", "", "No coding agents found"), el("span", "", "Install Claude Code, Codex, Gemini CLI, OpenCode… and dial will list them here."));
    list.append(e);
  }
  for (const a of state.agents) {
    const row = el("div", "row agent");
    row.dataset.id = a.id;
    row.title = a.path;
    const who = el("div", "who");
    who.append(el("div", "name", a.name));
    const fields = el("div", "fields");
    for (const f of a.fields) {
      const b = el("button", "field");
      const opt = optionFor(f, f.value);
      b.title = `${f.label}: ${f.value || "agent default"}` + (opt?.note ? ` · ${opt.note}` : "");
      if (opt?.icon) b.append(icon(opt.icon));
      else if (f.label !== "model" || !f.value) b.append(el("span", "k", f.label));
      b.append(el("span", "v" + (f.value ? "" : " empty"), opt?.label || f.value || "default"));
      const c = el("span", "chev");
      c.append(svg(CHEV, 11, 1.7));
      b.append(c);
      b.onclick = (ev) => openPicker(a, f, b, ev);
      fields.append(b);
    }
    row.append(icon(a.icon), who, fields);
    list.append(row);
  }

  const chips = $("#profiles");
  chips.replaceChildren();
  if (!state.profiles.length) chips.append(el("span", "hint", "Save the current setup to switch everything back in one click."));
  for (const p of state.profiles) {
    const c = el("button", "chip");
    c.title = p.summary;
    c.append(el("span", "", p.name));
    const x = el("span", "x", "×");
    x.title = "Delete profile";
    x.onclick = (ev) => { ev.stopPropagation(); profileAction("delete", p.name); };
    c.append(x);
    c.onclick = () => profileAction("use", p.name);
    chips.append(c);
  }
  fit();
}

// The tray panel has no scrollbars to speak of, so it grows to fit instead.
function fit() {
  if (mode !== "panel") return;
  const h = $(".top").offsetHeight + $("#agents").offsetHeight + $(".profiles").offsetHeight + $(".foot").offsetHeight + 4;
  if (h !== fit.last) { fit.last = h; api("window/fit?h=" + h, {}); }
}

async function load() {
  try {
    state = await api("state");
    renderAgents();
    if (view === "providers") await loadProviders();
    if (view === "usage") await loadUsage();
  } catch (e) {
    status(e.message, "err");
  }
}

// ---------- picker ----------

function score(q, o) {
  if (!q) return 1;
  const lv = o.value.toLowerCase(), ll = (o.label || "").toLowerCase();
  if (lv === q || ll === q) return 100;
  if (lv.startsWith(q) || ll.startsWith(q)) return 60;
  if (lv.includes(q) || ll.includes(q)) return 40;
  let i = 0;
  for (const ch of lv) if (ch === q[i]) i++;
  if (i === q.length) return 20;
  if ((o.note || "").toLowerCase().includes(q) || (o.group || "").toLowerCase().includes(q)) return 10;
  return 0;
}

function placePop(anchor, w, h) {
  const pop = $("#pop");
  const r = anchor.getBoundingClientRect(), pad = 8;
  pop.style.width = w + "px";
  let x = Math.min(r.left, innerWidth - w - pad);
  let y = r.bottom + 5;
  pop.classList.remove("up");
  if (y + h > innerHeight - pad && r.top - 5 - h >= pad) { y = r.top - 5 - h; pop.classList.add("up"); }
  else if (y + h > innerHeight - pad) y = Math.max(pad, innerHeight - pad - h);
  pop.style.left = Math.max(pad, x) + "px";
  pop.style.top = y + "px";
}

// openPicker drops the option list under a field button. `only` narrows the
// options (the providers page offers one vendor's models at a time).
function openPicker(agent, field, anchor, ev, only) {
  ev.stopPropagation();
  closePicker();
  const cur = field.value;
  let options = field.options.filter((o) => !only || only(o));
  // current value first, then the rest in catalog order
  const i = options.findIndex((o) => o.value === cur);
  if (i > 0) { const [c] = options.splice(i, 1); options.unshift({ ...c, group: "" }); }
  else if (i < 0 && cur && !only) options.unshift({ value: cur, note: "current value" });
  // the agent's own default: dial's wiring comes out and the key is removed
  if (!only) options.unshift({ value: "", label: "Default", note: `what ${agent.name} ships with`, icon: agent.icon, reset: true });
  pick = { agent, field, options, anchor, cursor: 0, free: !only };
  anchor.classList.add("open");
  const pop = $("#pop");
  pop.hidden = false;
  placePop(anchor, options.some((o) => o.note && o.note !== o.value) ? 372 : 300, 340);
  const q = $("#q");
  q.value = "";
  q.placeholder = field.label === "model" ? (only ? "Filter models…" : "Filter, or type any model id…") : `Filter ${field.label}…`;
  filter();
  q.focus();
}

function filter() {
  if (!pick) return;
  const q = $("#q").value.trim().toLowerCase();
  const scored = pick.options.map((o, i) => ({ o, i, s: score(q, o) })).filter((x) => x.s > 0);
  // with a query, best matches first; without, catalog order keeps the groups together
  if (q) scored.sort((a, b) => b.s - a.s || a.i - b.i);
  pick.items = scored.map((x) => x.o);
  const typed = $("#q").value.trim();
  if (typed && pick.free && pick.field.label === "model" && !pick.items.some((o) => o.value === typed)) {
    pick.items.push({ value: typed, note: "use as typed", custom: true });
  }
  pick.cursor = 0;
  renderList();
}

function renderList() {
  const list = $("#list");
  list.replaceChildren();
  if (!pick.items.length) { list.append(el("div", "none", "No matches.")); return; }
  const hasIcons = pick.items.some((o) => o.icon);
  const q = $("#q").value.trim();
  let group = null;
  pick.items.forEach((o, idx) => {
    if (!q && o.group && o.group !== group) list.append(el("li", "group", o.group));
    if (!q) group = o.group ?? group;
    const li = el("li", (idx === pick.cursor ? "sel" : "") + (o.value === pick.field.value ? " cur" : "") + (o.custom ? " custom" : "") + (o.reset ? " reset" : ""));
    li.dataset.i = idx;
    if (hasIcons) li.append(icon(o.icon));
    li.append(el("span", "v", o.label || o.value));
    let note = o.note && o.note !== (o.label || o.value) ? o.note : "";
    if (q && o.group && !note) note = o.group;
    if (note) li.append(el("span", "n", note));
    const ck = el("span", "check");
    ck.append(svg(CHECK, 12, 1.8));
    li.append(ck);
    li.onmousemove = () => { if (pick.cursor !== idx) { pick.cursor = idx; renderList(); } };
    li.onclick = () => commit(o.value);
    list.append(li);
  });
  list.querySelector(`li[data-i="${pick.cursor}"]`)?.scrollIntoView({ block: "nearest" });
}

function move(d) {
  if (!pick || !pick.items.length) return;
  pick.cursor = (pick.cursor + d + pick.items.length) % pick.items.length;
  renderList();
}

async function commit(value) {
  if (!pick || value == null) return;
  const { agent, field } = pick;
  const opt = pick.options.find((o) => o.value === value);
  closePicker();
  if (value === field.value) return;
  try {
    state = await api("set", { agent: agent.id, field: field.key, value });
    renderAgents();
    const b = document.querySelector(`.agent[data-id="${agent.id}"] .field:nth-child(${agent.fields.indexOf(field) + 1})`);
    b?.classList.add("flash");
    const shown = opt?.label || value;
    if (state.notice) status(`${agent.name} → ${shown}. ${state.notice}`, "warn", 9000);
    else status(`${agent.name} ${field.label} → ${shown}`, "ok");
    if (providers) loadProviders();
  } catch (e) {
    status(e.message, "err");
  }
}

function closePicker() {
  if (!pick) return;
  pick.anchor.classList.remove("open");
  $("#pop").hidden = true;
  pick = null;
}

$("#q").addEventListener("input", filter);
$("#q").addEventListener("keydown", (e) => {
  if (e.key === "ArrowDown" || (e.ctrlKey && e.key === "n")) { e.preventDefault(); move(1); }
  else if (e.key === "ArrowUp" || (e.ctrlKey && e.key === "p")) { e.preventDefault(); move(-1); }
  else if (e.key === "Enter") { e.preventDefault(); commit(pick?.items[pick.cursor]?.value); }
  else if (e.key === "Escape") { e.preventDefault(); e.stopPropagation(); closePicker(); }
});
document.addEventListener("mousedown", (e) => { if (pick && !$("#pop").contains(e.target)) closePicker(); });
document.addEventListener("keydown", (e) => {
  if (e.key === "Escape" && !pick && mode === "panel") api("window/hide", {});
});

// ---------- profiles ----------

async function profileAction(action, name) {
  try {
    const data = await api("profile/" + action, { name });
    state = data;
    renderAgents();
    if (action === "use") status(`${name} applied · ${data.changed} setting${data.changed === 1 ? "" : "s"} changed`, "ok");
    else if (action === "save") status(`Saved ${name}`, "ok");
    else status(`Deleted ${name}`);
  } catch (e) {
    status(e.message, "err");
  }
}

$("#save").onclick = () => {
  const chips = $("#profiles");
  if (chips.querySelector(".chip-input")) return;
  const input = el("input", "chip-input");
  input.placeholder = "Profile name";
  input.onkeydown = (e) => {
    if (e.key === "Enter" && input.value.trim()) profileAction("save", input.value.trim());
    else if (e.key === "Escape") input.remove();
    e.stopPropagation();
  };
  input.onblur = () => setTimeout(() => input.remove(), 100);
  chips.prepend(input);
  input.focus();
};

// ---------- providers view ----------
//
// A provider is a vendor plus the key the user pasted. Presets need only the
// key; a custom one needs a name and a base URL too. Every exposed model of
// every provider becomes "provider/model" in the agents' pickers, served by
// the local gateway in whichever API the agent speaks.

async function loadProviders() {
  providers = await api("providers");
  if (!providers.providers.length && editing === null) adding = true;
  renderProviders();
}

// One row per provider: logo, name, the agents pointed at it, key status.
// Everything else lives in the editor that opens under the row.
function renderProviders() {
  renderGateway();
  const list = $("#providers");
  list.replaceChildren();
  list.hidden = !providers.providers.length;
  for (const p of providers.providers) {
    const open = editing === p.id;
    const row = el("div", "row provider" + (open ? " selected" : ""));
    row.dataset.id = p.id;
    const who = el("div", "who");
    const name = el("div", "name", p.name);
    if (p.sponsored) name.append(el("span", "badge", "sponsored"));
    const n = p.models.filter((m) => m.on).length;
    who.append(name, el("div", "sub", p.host + " · " + (n ? `${n} model${n === 1 ? "" : "s"}` : "no models exposed")));
    const using = p.agents.filter((a) => a.current);
    const uses = el("div", "uses");
    for (const a of using) {
      const b = el("button", "use");
      b.title = `${a.name} · ${a.model} — click to change`;
      b.append(icon(a.icon));
      b.onclick = (ev) => pickForAgent(a, p, b, ev);
      uses.append(b);
    }
    if (p.agents.some((a) => !a.current && canUse(a, p))) {
      const b = el("button", "use add");
      b.title = using.length ? "Point another agent at " + p.name : "Point an agent at " + p.name;
      b.append(svg(PLUS, 11, 1.8));
      b.onclick = (ev) => { ev.stopPropagation(); editing = p.id; draft = null; adding = false; renderProviders(); };
      uses.append(b);
    }
    const key = el("span", "key " + (p.key.set ? "on" : p.ready ? "free" : "none"), p.key.set ? p.key.masked : p.ready ? "no key" : "needs a key");
    key.title = p.key.set ? "API key " + p.key.masked : p.ready ? "Local servers need no key" : "Open the row and paste an API key";
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(icon(p.icon || "generic"), who, uses, key, chev);
    row.onclick = () => { editing = open ? null : p.id; draft = null; adding = false; renderProviders(); };
    list.append(row);
    if (open) list.append(renderEditor(p));
  }
  renderAdd();
  renderActivity();
  (list.querySelector(".editor") || $("#addSheet .editor"))?.scrollIntoView({ block: "nearest" });
}

// The gateway: where every agent sends its requests. Opens to the recent calls.
function renderGateway() {
  const g = providers.gateway;
  const box = $("#gateway");
  box.replaceChildren();
  box.classList.toggle("selected", activity);
  const dot = el("span", "dot " + (g.running ? "on" : ""));
  const who = el("div", "who");
  const name = el("div", "name", "Gateway");
  name.append(el("span", "state", g.running ? (g.mine ? "running" : "running · another dial") : "not running"));
  who.append(name, el("div", "sub", g.running ? `${g.models} model${g.models === 1 ? "" : "s"} · OpenAI, Responses and Anthropic APIs` : "start it with dial serve, or open dial at login"));
  const url = el("button", "url");
  url.append(el("code", "", g.url + "/v1"));
  url.title = "Copy the base URL";
  url.onclick = async (ev) => { ev.stopPropagation(); try { await navigator.clipboard.writeText(g.url + "/v1"); status("Base URL copied", "ok"); } catch { status(g.url + "/v1"); } };
  const chev = el("span", "chev");
  chev.append(svg(CHEV_R, 11, 1.7));
  chev.title = activity ? "Hide recent calls" : "Recent calls";
  box.append(dot, who, url, chev);
  box.onclick = () => { activity = !activity; renderProviders(); };
}

function renderActivity() {
  const box = $("#activity");
  box.replaceChildren();
  box.hidden = !activity;
  if (!activity) return;
  const calls = providers.gateway.calls.slice(0, 12);
  if (!calls.length) { box.append(el("div", "none", "No requests yet. Point an agent at a model here and use it; its calls show up as they happen.")); return; }
  for (const c of calls) {
    const r = el("div", "call" + (c.status >= 400 ? " bad" : ""));
    r.append(el("span", "when", new Date(c.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })));
    r.append(el("span", "m", c.model));
    r.append(el("span", "p", c.from === c.to ? c.from : `${c.from} → ${c.to}`));
    r.append(el("span", "grow"));
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${c.ms} ms`));
    r.title = c.error || `${c.provider} · ${c.ms} ms`;
    box.append(r);
  }
}

// An agent's model field, and whether any of its options come from provider p.
function modelField(a) {
  const agent = state.agents.find((x) => x.id === a.id);
  return agent?.fields.find((f) => f.key === "model") || null;
}
function ofProvider(p) { return new RegExp(`^(dial/)?${p.id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/`); }
function canUse(a, p) { const f = modelField(a); return !!f && f.options.some((o) => ofProvider(p).test(o.value)); }

// Pick one of this provider's models for an agent, straight from the row.
function pickForAgent(a, p, btn, ev) {
  const agent = state.agents.find((x) => x.id === a.id);
  const field = modelField(a);
  if (!field) return;
  const pre = ofProvider(p);
  if (!field.options.some((o) => pre.test(o.value))) {
    ev.stopPropagation();
    status(`${p.name} exposes no models yet — pick some below`, "warn");
    editing = p.id; draft = null; renderProviders();
    return;
  }
  openPicker(agent, field, btn, ev, (o) => pre.test(o.value));
}

// The add sheet: presets first (a key is all they need), custom last.
let presetQuery = "";
function renderAdd() {
  const sheet = $("#addSheet");
  sheet.replaceChildren();
  sheet.hidden = !adding;
  $("#addProvider").hidden = adding;
  if (!adding) return;
  const head = el("div", "row-head");
  head.append(el("span", "label", providers.providers.length ? "Add a provider" : "Add your first provider"), el("span", "grow"));
  const q = input(presetQuery, "Find a vendor…");
  q.className = "find";
  q.oninput = () => { presetQuery = q.value; drawTiles(); };
  head.append(q);
  if (providers.providers.length) {
    const x = el("button", "text", "Close");
    x.onclick = () => { adding = false; editing = null; draft = null; presetQuery = ""; renderProviders(); };
    head.append(x);
  }
  sheet.append(head);
  const tiles = el("div", "tiles");
  sheet.append(tiles);
  const drawTiles = () => {
    tiles.replaceChildren();
    const f = presetQuery.trim().toLowerCase();
    const hit = (pr) => !f || pr.name.toLowerCase().includes(f) || pr.id.includes(f) || hostOf(pr.chat || pr.responses || pr.anthropic).includes(f) || (pr.note || "").toLowerCase().includes(f);
    let any = false;
    for (const [kind, title] of [["vendor", "Vendors"], ["relay", "Relays · many vendors behind one key"], ["local", "On this machine"]]) {
      const ps = providers.presets.filter((p) => p.kind === kind && hit(p));
      if (!ps.length && !(kind === "local" && !f)) continue;
      any = true;
      tiles.append(el("div", "kind", title));
      const grid = el("div", "grid");
      for (const pr of ps) grid.append(tile(pr));
      if (kind === "local" && !f) {
        const c = el("button", "tile custom" + (editing?.custom ? " on" : ""));
        const ic = el("span", "ic plus");
        ic.append(svg(PLUS, 13, 1.8));
        c.append(ic, el("span", "tt"));
        c.lastChild.append(el("span", "n", "Custom"), el("span", "s", "any compatible URL"));
        c.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
        grid.append(c);
      }
      tiles.append(grid);
    }
    if (!any) {
      const none = el("div", "none");
      none.append(`Nothing called “${presetQuery.trim()}”. `);
      const b = el("button", "link", "Add it as a custom provider");
      b.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
      none.append(b);
      tiles.append(none);
    }
  };
  drawTiles();
  if (editing && typeof editing === "object") sheet.append(renderEditor(null, editing.preset));
}

function tile(pr) {
  const t = el("button", "tile" + (pr.added ? " added" : "") + (editing?.preset === pr.id ? " on" : ""));
  t.append(icon(pr.icon || "generic"));
  const tt = el("span", "tt");
  const n = el("span", "n", pr.name);
  if (pr.sponsored) n.append(el("span", "badge", "sponsored"));
  tt.append(n, el("span", "s", pr.note || hostOf(pr.chat || pr.responses || pr.anthropic)));
  t.append(tt);
  if (pr.added) {
    const ck = el("span", "check");
    ck.append(svg(CHECK, 10, 2));
    t.append(ck);
    t.title = `${pr.name} is already added — open it`;
    t.onclick = () => { editing = pr.id; adding = false; draft = null; renderProviders(); };
  } else {
    t.onclick = () => { editing = { preset: pr.id }; draft = null; renderProviders(); };
  }
  return t;
}

function field(label, control, hint) {
  const l = el("label", "", label);
  const wrap = el("div");
  wrap.append(control);
  if (hint) wrap.append(el("div", "hint", hint));
  return [l, wrap];
}
function input(value, placeholder, type = "text") {
  const i = el("input");
  i.type = type;
  i.value = value || "";
  i.placeholder = placeholder || "";
  i.spellcheck = false;
  i.autocomplete = "off";
  i.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Escape") cancelEdit(); };
  return i;
}
function cancelEdit() { editing = null; draft = null; renderProviders(); }

const PROTOS = [["chat", "OpenAI", "Chat Completions — most agents"], ["responses", "Responses", "OpenAI Responses — what Codex speaks"], ["anthropic", "Anthropic", "Anthropic Messages — what Claude Code speaks"]];

// renderEditor: an existing provider (p), a new preset (presetID), or custom.
function renderEditor(p, presetID) {
  const pr = presetID ? providers.presets.find((x) => x.id === presetID) : p?.preset ? providers.presets.find((x) => x.id === p.preset) : null;
  const isNew = !p, custom = !pr;
  draft = draft || (p
    ? { id: p.id, name: p.name, preset: p.preset, chat: p.chat, responses: p.responses, anthropic: p.anthropic, catalog: p.catalog, key: "", api: p.anthropic && !p.chat ? "anthropic" : "openai", chosen: p.models.filter((m) => m.on).map((m) => m.id), extra: [] }
    : pr
      ? { id: pr.id, name: pr.name, preset: pr.id, key: "", chosen: [], extra: [] }
      : { id: "", name: "", preset: "", chat: "", responses: "", anthropic: "", catalog: "", key: "", api: "openai", chosen: [], extra: [] });
  const ed = el("div", "editor" + (isNew ? " new" : ""));
  ed.onclick = (e) => e.stopPropagation();

  if (isNew) {
    const h = el("div", "ehead");
    h.append(icon(pr?.icon || "generic"), el("b", "", pr ? pr.name : "Custom provider"));
    if (pr?.note) h.append(el("span", "note", pr.note));
    h.append(el("span", "grow"));
    if (pr?.website) { const b = el("button", "link", hostOf(pr.website) + " ↗"); b.onclick = () => api("open", { url: pr.website }); h.append(b); }
    ed.append(h);
  }

  // who uses it: every agent, the ones on this provider lit, click to pick a model
  if (p) {
    const chips = el("div", "achips");
    for (const a of p.agents) {
      if (!a.current && !canUse(a, p)) continue; // Gemini CLI, Cursor: their own APIs only
      const c = el("button", "achip" + (a.current ? " on" : ""));
      c.append(icon(a.icon), el("span", "n", a.name));
      if (a.current) c.append(el("span", "m", a.model));
      c.title = a.current ? `${a.name} is on ${a.model} — click to change` : `Point ${a.name} at a ${p.name} model`;
      c.onclick = (ev) => pickForAgent(a, p, c, ev);
      chips.append(c);
    }
    ed.append(...field("Agents", chips, p.agents.some((a) => a.current) ? "" : "None yet. Click an agent to pick one of these models for it."));
  }

  let name, url;
  if (custom) {
    name = input(draft.name, "e.g. My Relay");
    name.oninput = () => { draft.name = name.value; if (isNew) draft.id = slug(name.value); };
    ed.append(...field("Name", name));

    const seg = el("div", "segs");
    for (const [v, l, hint] of [["openai", "OpenAI compatible", "…/v1 — chat completions, and responses when the vendor has it"], ["anthropic", "Anthropic compatible", "the root URL, what ANTHROPIC_BASE_URL would take"]]) {
      const b = el("button", "opt" + (draft.api === v ? " on" : ""), l);
      b.title = hint;
      b.onclick = () => { draft.api = v; const u = url.value; if (v === "anthropic") { draft.anthropic = u; draft.chat = ""; } else { draft.chat = u; draft.anthropic = ""; } for (const x of seg.children) x.classList.toggle("on", x === b); url.placeholder = v === "anthropic" ? "https://…" : "https://…/v1"; };
      seg.append(b);
    }
    url = input(draft.api === "anthropic" ? draft.anthropic : draft.chat, draft.api === "anthropic" ? "https://…" : "https://…/v1", "url");
    url.oninput = () => { if (draft.api === "anthropic") draft.anthropic = url.value; else draft.chat = url.value; };
    const urlWrap = el("div", "stack");
    urlWrap.append(seg, url);
    ed.append(...field("Base URL", urlWrap));
  }

  const key = input("", p?.key.set ? `${p.key.masked} · paste a new key to replace it` : pr?.noKey || p?.key.optional ? "optional for local servers" : "paste an API key", "password");
  key.oninput = () => { draft.key = key.value; };
  key.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter" && isNew) save(); else if (e.key === "Escape") cancelEdit(); };
  const side = el("div", "side");
  const eye = el("button", "text", "Show");
  let revealed = false; // the saved key is in the box, not a draft
  eye.onclick = async () => {
    if (key.type === "password") {
      if (!key.value && p?.key.set) {
        try { key.value = (await api("provider/key", { id: p.id })).key; revealed = true; } catch (e) { status(e.message, "err"); return; }
      }
      key.type = "text"; eye.textContent = "Hide";
    } else {
      if (revealed && !draft.key) key.value = "";
      revealed = false;
      key.type = "password"; eye.textContent = "Show";
    }
  };
  side.append(eye);
  const keysUrl = p?.keysUrl || pr?.keysUrl;
  if (keysUrl) { const b = el("button", "link", "Get a key ↗"); b.onclick = () => api("open", { url: keysUrl }); side.append(b); }
  const keyWrap = el("div", "pair");
  keyWrap.append(key, side);
  ed.append(...field("API key", keyWrap, isNew ? "Kept in ~/.config/dial/providers.json, readable by you alone. Nothing is read from your shell." : ""));

  if (p) ed.append(...field("Models", renderModels(p), ""));
  else if (custom) {
    const ex = input("", "model ids, comma separated · e.g. gpt-5.5, claude-sonnet-5");
    ex.oninput = () => { draft.extra = ex.value.split(/[,\s]+/).filter(Boolean); };
    ed.append(...field("Models", ex, "Optional: dial asks the vendor for its list after saving."));
  }

  // the endpoints, with a Test that reports against each one
  const eps = el("div", "eps");
  const slots = {};
  if (!custom) {
    const src = p || pr;
    for (const [proto, label, hint] of PROTOS) {
      if (!src[proto]) continue;
      const e = el("div", "ep");
      const pl = el("span", "pl", label);
      pl.title = hint;
      e.append(pl, el("code", "", src[proto]), slots[proto] = el("span", "res"));
      eps.append(e);
    }
    if (p) {
      const test = el("button", "text", "Test");
      test.title = "Send a tiny request through each endpoint";
      test.onclick = async () => {
        test.classList.add("busy");
        for (const s of Object.values(slots)) { s.className = "res wait"; s.textContent = "…"; }
        try {
          const r = await api("provider/test", { id: p.id });
          for (const t of r.results) {
            const s = slots[t.protocol];
            if (!s) continue;
            s.className = "res " + (t.ok ? "ok" : "bad");
            s.replaceChildren();
            s.append(svg(t.ok ? CHECK : "M4.5 4.5l7 7M11.5 4.5l-7 7", 10, 2));
            s.append(el("span", "", t.ok ? `${t.ms} ms` : t.status ? `${t.status} · ${t.error}` : t.error));
            s.title = t.ok ? `model ${t.model}` : t.error;
          }
        } catch (e) { for (const s of Object.values(slots)) { s.className = "res"; s.textContent = ""; } status(e.message, "err"); }
        test.classList.remove("busy");
      };
      eps.append(test);
    }
    ed.append(...field("Endpoints", eps));
  }

  if (custom) {
    const more = el("details", "more");
    more.append(el("summary", "", "More endpoints"));
    const inner = el("div", "inner");
    const add = (label, key, ph, hint) => {
      const i = input(draft[key], ph, "url");
      i.oninput = () => { draft[key] = i.value; };
      inner.append(...field(label, i, hint));
    };
    if (draft.api === "anthropic") add("OpenAI URL", "chat", "https://…/v1", "if the vendor also serves chat completions");
    else add("Anthropic URL", "anthropic", "https://…", "if the vendor also serves Anthropic messages");
    add("Responses URL", "responses", "https://…/v1", "if the vendor serves the OpenAI Responses API (Codex uses it natively)");
    const cat = input(draft.catalog, "models.dev id, e.g. openai");
    cat.oninput = () => { draft.catalog = cat.value; };
    inner.append(...field("Catalog", cat, "Display names and reasoning levels for the models"));
    more.append(inner);
    ed.append(more);
  }

  const bar = el("div", "bar");
  if (p) {
    const del = el("button", "text danger", "Remove");
    del.onclick = () => providerAction("delete", { id: p.id }, `${p.name} removed`);
    bar.append(del);
  }
  bar.append(el("span", "grow"));
  const cancel = el("button", "text", "Cancel");
  cancel.onclick = cancelEdit;
  const saveBtn = el("button", "text primary", isNew ? "Add" : "Save");
  const save = () => {
    const body = { id: draft.id, name: draft.name, preset: draft.preset, key: draft.key || "", chat: draft.chat, responses: draft.responses, anthropic: draft.anthropic, catalog: draft.catalog, models: p ? draft.chosen : draft.extra };
    if (isNew && custom && !body.name) { name.focus(); return status("Give it a name", "warn"); }
    if (isNew && custom && !body.chat && !body.anthropic) { url.focus(); return status("A base URL is needed", "warn"); }
    saveBtn.classList.add("busy");
    providerAction("save", body, `${draft.name || draft.id} ${isNew ? "added" : "saved"}`, body.id || slug(body.name));
  };
  saveBtn.onclick = save;
  bar.append(cancel, saveBtn);
  ed.append(bar);
  setTimeout(() => (isNew ? (custom ? name : key) : null)?.focus(), 0);
  return ed;
}

// Which of the vendor's models the agents get to see: click to toggle, type
// to add one the vendor's list lacks, Refresh to ask the vendor again.
function renderModels(p) {
  const box = el("div", "models");
  const chips = el("div", "mchips");
  const q = p.models.length > 24 ? input("", `filter ${p.models.length} models…`) : null;
  const draw = () => {
    chips.replaceChildren();
    const f = (q?.value || "").trim().toLowerCase();
    let shown = 0;
    for (const m of p.models) {
      const on = draft.chosen.includes(m.id);
      if (f && !m.id.toLowerCase().includes(f) && !(m.name || "").toLowerCase().includes(f) && !on) continue;
      const c = el("button", "mchip" + (on ? " on" : ""));
      c.append(el("span", "", m.name && m.name !== m.id ? m.name : m.id));
      if (m.name && m.name !== m.id) c.title = m.id;
      c.onclick = () => { draft.chosen = on ? draft.chosen.filter((x) => x !== m.id) : [...draft.chosen, m.id]; draw(); };
      chips.append(c);
      if (++shown >= 80 && !f) { chips.append(el("span", "hint", `… ${p.models.length - shown} more, filter to find them`)); break; }
    }
    for (const id of draft.chosen) {
      if (p.models.some((m) => m.id === id)) continue;
      const c = el("button", "mchip on own");
      c.append(el("span", "", id));
      c.title = "Added by hand";
      c.onclick = () => { draft.chosen = draft.chosen.filter((x) => x !== id); draw(); };
      chips.append(c);
    }
    if (!p.models.length && !draft.chosen.length) chips.append(el("span", "hint", "The vendor's list is empty. Refresh, or type a model id."));
  };
  if (q) { q.oninput = draw; box.append(q); }
  box.append(chips);
  const foot = el("div", "mfoot");
  const add = input("", "add a model id…");
  add.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && add.value.trim()) { const id = add.value.trim(); if (!draft.chosen.includes(id)) draft.chosen.push(id); add.value = ""; draw(); }
    else if (e.key === "Escape") cancelEdit();
  };
  const refresh = el("button", "text", "Refresh");
  refresh.title = "Ask the vendor which models it serves";
  refresh.onclick = async () => {
    refresh.classList.add("busy");
    try {
      const r = await api("provider/models", { id: p.id });
      status(`${p.name}: ${r.count} models`, "ok");
      const chosen = draft.chosen;
      await loadProviders();
      draft = draft || {};
      draft.chosen = chosen;
      renderProviders();
    } catch (e) { status(e.message, "err"); refresh.classList.remove("busy"); }
  };
  foot.append(add, refresh);
  if (p.fetched) foot.append(el("span", "hint", `vendor list · ${p.fetched}`));
  else if (p.models.length) foot.append(el("span", "hint", "from models.dev · Refresh asks the vendor"));
  box.append(foot);
  draw();
  return box;
}

async function providerAction(action, body, okMsg, keep) {
  try {
    providers = await api("provider/" + action, body);
    editing = action === "save" && keep ? keep : null;
    draft = null;
    adding = false;
    presetQuery = "";
    renderProviders();
    state = await api("state");
    renderAgents();
    if (okMsg) status(okMsg, "ok");
  } catch (e) {
    status(e.message, "err");
    document.querySelector(".editor .busy")?.classList.remove("busy");
  }
}

function slug(s) { return s.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, ""); }
function hostOf(u) { try { return new URL(u.includes("://") ? u : "https://" + u).host; } catch { return ""; } }

$("#addProvider").onclick = () => { adding = true; editing = null; draft = null; renderProviders(); };

// ---------- usage ----------

const PERIODS = [["today", "Today"], ["7d", "7 days"], ["30d", "30 days"], ["all", "All"]];

async function loadUsage() {
  usage = await api("usage?period=" + period);
  renderUsage();
}

function fmtN(n) {
  if (n >= 1e9) return (n / 1e9).toFixed(2) + "B";
  if (n >= 1e7) return Math.round(n / 1e6) + "M";
  if (n >= 1e6) return (n / 1e6).toFixed(1) + "M";
  if (n >= 1e5) return Math.round(n / 1e3) + "K";
  if (n >= 1e3) return (n / 1e3).toFixed(1) + "K";
  return String(n);
}
function fmtCost(t) {
  if (!t.cost && t.unpriced) return "";
  const c = t.cost;
  const s = c >= 100 ? c.toFixed(0) : c >= 1 ? c.toFixed(2) : c.toFixed(3);
  return "$" + s + (t.unpriced ? "+" : "");
}
const tokensOf = (t) => t.input + t.output;

function renderUsage() {
  const u = usage;
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), name);
    b.onclick = () => { period = id; loadUsage().catch((e) => status(e.message, "err")); };
    seg.append(b);
  }
  const cost = $("#usageCost");
  cost.replaceChildren();
  const c = fmtCost(u);
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", "list price"));
    cost.title = u.unpriced ? `${u.unpriced} call${u.unpriced === 1 ? "" : "s"} had no known price and are not counted` : "At each model's list price on models.dev";
  } else if (u.calls) {
    cost.append(el("span", "", "no price for these models"));
  }

  const stats = $("#stats");
  stats.replaceChildren();
  const empty = !u.calls;
  $("#chart").hidden = empty;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = empty;
  for (const h of $$("#view-usage .row-head")) h.hidden = empty;
  if (empty) {
    stats.classList.add("empty");
    const when = { today: "today", "7d": "in the last 7 days", "30d": "in the last 30 days", all: "yet" }[period];
    stats.append(el("div", "none", `No calls ${when}. Point an agent at a catalog model and use it; every call through the gateway is counted here.`));
    $("#usageNote").textContent = "";
    return;
  }
  stats.classList.remove("empty");
  const tile = (n, label, sub) => {
    const t = el("div", "kpi");
    t.append(el("b", "", n), el("span", "", label));
    if (sub) t.append(el("small", "", sub));
    stats.append(t);
  };
  tile(fmtN(tokensOf(u)), "tokens", `${fmtN(u.input)} in · ${fmtN(u.output)} out`);
  tile(fmtN(u.cache_read), "cache read", u.cache_write ? `${fmtN(u.cache_write)} written` : "");
  tile(fmtN(u.reasoning), "reasoning", "inside output");
  tile(String(u.calls), u.calls === 1 ? "call" : "calls", u.errors ? `${u.errors} failed` : "");

  // the timeline: one bar per hour, day or week; output sits on top of input
  const chart = $("#chart");
  chart.replaceChildren();
  const bars = el("div", "bars");
  const peak = Math.max(1, ...u.series.map(tokensOf));
  const labels = el("div", "labels");
  const n = u.series.length;
  const every = n <= 8 ? 1 : n <= 31 ? Math.ceil(n / 6) : Math.ceil(n / 5);
  u.series.forEach((p, i) => {
    const b = el("div", "bar");
    const inp = el("i", "in"), out = el("i", "out");
    inp.style.height = (100 * p.input / peak).toFixed(1) + "%";
    out.style.height = (100 * p.output / peak).toFixed(1) + "%";
    b.append(out, inp);
    const when = u.bucket === "hour" ? `${p.label}:00` : (u.bucket === "week" ? "week of " : "") + p.label;
    b.title = p.calls ? `${when} · ${fmtN(tokensOf(p))} tokens · ${p.calls} call${p.calls === 1 ? "" : "s"}${fmtCost(p) ? " · ≈" + fmtCost(p) : ""}` : `${when} · nothing`;
    bars.append(b);
    const last = i === n - 1 && (n - 1) % every >= every / 2;
    labels.append(el("span", "", i % every === 0 || last ? p.label : ""));
  });
  chart.append(el("div", "peak", fmtN(peak)), bars, labels);

  const total = Math.max(1, tokensOf(u));
  const list = (id, groups) => {
    const box = $("#" + id);
    box.replaceChildren();
    for (const g of groups) {
      const r = el("div", "row stat");
      r.append(icon(g.icon || "generic"));
      const who = el("div", "who");
      who.append(el("div", "name", g.name));
      const sub = [];
      if (g.sub) sub.push(g.sub);
      sub.push(`${g.calls} call${g.calls === 1 ? "" : "s"}`);
      if (g.errors) sub.push(`${g.errors} failed`);
      who.append(el("div", "sub", sub.join(" · ")));
      r.append(who);
      const share = el("div", "share");
      const fill = el("i");
      fill.style.width = Math.max(1.5, 100 * tokensOf(g) / total).toFixed(1) + "%";
      share.append(fill);
      share.title = Math.round(100 * tokensOf(g) / total) + "% of tokens";
      r.append(share);
      const num = el("div", "num");
      num.append(el("b", "", fmtN(tokensOf(g))), el("small", "", `${fmtN(g.input)} in · ${fmtN(g.output)} out${g.cache_read ? " · " + fmtN(g.cache_read) + " cached" : ""}`));
      r.append(num);
      r.append(el("div", "cost", fmtCost(g) ? "≈" + fmtCost(g) : ""));
      box.append(r);
    }
  };
  list("usageAgents", u.agents);
  list("usageModels", u.models);
  $("#usageNote").textContent = `Counted from the providers' own usage reports on every call through the gateway · ${u.path}`;
}

// ---------- header / footer ----------

function show(v) {
  view = v;
  for (const b of $("#nav").children) b.classList.toggle("on", b.dataset.view === v);
  $("#view-agents").hidden = v !== "agents";
  $("#view-providers").hidden = v !== "providers";
  $("#view-usage").hidden = v !== "usage";
  closePicker();
  if (v === "providers") loadProviders().catch((e) => status(e.message, "err"));
  if (v === "usage") loadUsage().catch((e) => status(e.message, "err"));
}
for (const b of $("#nav").children) b.onclick = () => { show(b.dataset.view); b.blur(); };

$("#sync").onclick = async () => {
  const b = $("#sync");
  b.classList.add("spin");
  try {
    state = await api("sync", {});
    renderAgents();
    if (providers) await loadProviders();
    status("Model lists refreshed", "ok");
  } catch (e) {
    status("Sync failed: " + e.message, "err");
  } finally {
    b.classList.remove("spin");
  }
};
$("#open").onclick = () => api("window/main", {});
$("#openMain").onclick = () => api("window/main", {});
$("#quit").onclick = () => api("window/quit", {});
if (mode === "window") { $("#open").remove(); $("#openMain").remove(); $("#quit").remove(); }
else { $("#nav").remove(); }

// Config files may change underneath us (another dial, an editor); reload when
// the panel comes back into view.
document.addEventListener("visibilitychange", () => { if (!document.hidden) load(); });
window.addEventListener("focus", load);
if (mode === "window" && ["providers", "usage"].includes(params.get("view"))) show(params.get("view"));
load();
