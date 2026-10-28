// dial — one state object per view, rendered into a list. No framework.
const $ = (s) => document.querySelector(s);
const params = new URLSearchParams(location.search);
const mode = params.get("mode") || "window";
document.body.classList.add(mode);
if (params.get("theme")) document.documentElement.dataset.theme = params.get("theme");

let state = { agents: [], profiles: [], catalog: "" };
let providers = null; // { providers, presets, gateway }
let view = "agents";
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

// A brand icon: colour logos are images, mono logos take the text colour,
// anything unknown gets a two-letter monogram tinted by its name.
function icon(name, label) {
  const e = el("span", "ic");
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
  const w = (label || "?").split(/\s+/);
  e.classList.add("monogram");
  e.textContent = (w.length > 1 ? w[0][0] + w[1][0] : (label || "?").slice(0, 2)).toUpperCase();
  let h = 0;
  for (const ch of label || "") h = (h * 31 + ch.charCodeAt(0)) % 360;
  e.style.setProperty("--h", h);
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
      if (opt?.icon) b.append(icon(opt.icon, opt.label || f.value));
      else if (f.label !== "model" || !f.value) b.append(el("span", "k", f.label));
      b.append(el("span", "v" + (f.value ? "" : " empty"), opt?.label || f.value || "default"));
      const c = el("span", "chev");
      c.append(svg(CHEV, 11, 1.7));
      b.append(c);
      b.onclick = (ev) => openPicker(a, f, b, ev);
      fields.append(b);
    }
    row.append(icon(a.icon, a.name), who, fields);
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
    const li = el("li", (idx === pick.cursor ? "sel" : "") + (o.value === pick.field.value ? " cur" : "") + (o.custom ? " custom" : ""));
    li.dataset.i = idx;
    if (hasIcons) li.append(icon(o.icon, o.label || o.value));
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
  if (!pick || !value) return;
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

function renderProviders() {
  renderGateway();
  const list = $("#providers");
  list.replaceChildren();
  list.hidden = !providers.providers.length;
  for (const p of providers.providers) {
    const row = el("div", "row provider" + (editing === p.id ? " selected" : ""));
    row.dataset.id = p.id;
    const who = el("div", "who");
    const name = el("div", "name", p.name);
    if (p.sponsored) name.append(el("span", "badge", "sponsored"));
    const n = p.models.filter((m) => m.on).length;
    const sub = p.host + " · " + (n ? `${n} model${n === 1 ? "" : "s"}` : "no models exposed");
    who.append(name, el("div", "sub", sub));
    const uses = el("div", "uses");
    for (const a of p.agents) {
      const b = el("button", "use" + (a.current ? " on" : ""));
      b.title = a.current ? `${a.name} uses ${p.name} (${a.model})` : `Point ${a.name} at ${p.name}…`;
      b.append(icon(a.icon, a.name));
      b.onclick = (ev) => pickForAgent(a, p, b, ev);
      uses.append(b);
    }
    const key = el("span", "key " + (p.ready ? "on" : "none"), p.key.set ? p.key.masked : p.ready ? "no key needed" : "no key");
    key.title = p.key.set ? "API key " + p.key.masked : p.ready ? "Local servers need no key" : "Paste an API key";
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(icon(p.icon, p.name), who, uses, key, chev);
    row.onclick = () => { editing = editing === p.id ? null : p.id; draft = null; adding = false; renderProviders(); };
    list.append(row);
    if (editing === p.id) list.append(renderEditor(p));
  }
  renderAdd();
  renderActivity();
  (list.querySelector(".editor") || $("#addSheet .editor"))?.scrollIntoView({ block: "nearest" });
}

// The gateway strip: where agents send requests, and how many models answer.
function renderGateway() {
  const g = providers.gateway;
  const box = $("#gateway");
  box.replaceChildren();
  const dot = el("span", "dot " + (g.running ? "on" : ""));
  dot.title = g.running ? (g.mine ? "Served by this dial" : "Served by another dial process") : "Not running";
  const t = el("span", "t");
  t.append(el("b", "", "Gateway"), el("code", "", g.url + "/v1"));
  const sum = el("span", "sum", g.running ? `${g.models} model${g.models === 1 ? "" : "s"} · OpenAI, Responses and Anthropic APIs` : "not running · dial serve");
  const act = el("button", "text", activity ? "Hide activity" : "Activity");
  act.onclick = () => { activity = !activity; renderProviders(); };
  box.append(dot, t, sum, el("span", "grow"), act);
}

function renderActivity() {
  const box = $("#activity");
  box.replaceChildren();
  box.hidden = !activity;
  if (!activity) return;
  const calls = providers.gateway.calls.slice(0, 12);
  if (!calls.length) { box.append(el("div", "none", "No requests yet. Point an agent at a catalog model and use it; its calls show up here.")); return; }
  for (const c of calls) {
    const r = el("div", "call" + (c.status >= 400 ? " bad" : ""));
    const when = new Date(c.time);
    r.append(el("span", "when", when.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })));
    r.append(el("span", "m", c.model));
    r.append(el("span", "p", c.from === c.to ? c.from : `${c.from} → ${c.to}`));
    r.append(el("span", "grow"));
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${c.ms} ms`));
    r.title = c.error || `${c.provider} · ${c.ms} ms`;
    box.append(r);
  }
}

// Pick one of this provider's models for an agent, straight from the row.
function pickForAgent(a, p, btn, ev) {
  const agent = state.agents.find((x) => x.id === a.id);
  if (!agent || !agent.fields.length) return;
  const field = agent.fields[0];
  const pre = new RegExp(`^(dial/)?${p.id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/`);
  if (!field.options.some((o) => pre.test(o.value))) {
    ev.stopPropagation();
    status(`${p.name} exposes no models yet — pick some below`, "warn");
    editing = p.id; draft = null; renderProviders();
    return;
  }
  openPicker(agent, field, btn, ev, (o) => pre.test(o.value));
}

// The add sheet: presets first (a key is all they need), custom last.
function renderAdd() {
  const sheet = $("#addSheet");
  sheet.replaceChildren();
  sheet.hidden = !adding;
  $("#addProvider").hidden = adding;
  if (!adding) return;
  const head = el("div", "row-head");
  head.append(el("span", "label", providers.providers.length ? "Add a provider" : "Add your first provider"), el("span", "grow"));
  if (providers.providers.length) {
    const x = el("button", "text", "Close");
    x.onclick = () => { adding = false; editing = null; draft = null; renderProviders(); };
    head.append(x);
  }
  sheet.append(head);
  const kinds = [["vendor", "Vendors"], ["relay", "Relays · many vendors behind one key"], ["local", "On this machine"]];
  for (const [kind, title] of kinds) {
    const ps = providers.presets.filter((p) => p.kind === kind);
    if (!ps.length) continue;
    sheet.append(el("div", "kind", title));
    const grid = el("div", "grid");
    for (const pr of ps) grid.append(tile(pr));
    if (kind === "local") {
      const c = el("button", "tile custom" + (editing?.custom ? " on" : ""));
      const ic = el("span", "ic plus");
      ic.append(svg(PLUS, 14, 1.8));
      c.append(ic, el("span", "n", "Custom"), el("span", "s", "any compatible URL"));
      c.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
      grid.append(c);
    }
    sheet.append(grid);
  }
  if (editing && typeof editing === "object") sheet.append(renderEditor(null, editing.preset));
}

function tile(pr) {
  const t = el("button", "tile" + (pr.added ? " added" : "") + (editing?.preset === pr.id ? " on" : ""));
  t.append(icon(pr.icon, pr.name));
  const n = el("span", "n", pr.name);
  if (pr.sponsored) n.append(el("span", "badge", "sponsored"));
  t.append(n);
  t.append(el("span", "s", pr.note || hostOf(pr.chat || pr.responses || pr.anthropic)));
  if (pr.added) {
    const ck = el("span", "check");
    ck.append(svg(CHECK, 11, 2));
    t.append(ck);
    t.title = `${pr.name} is already added`;
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
    h.append(icon(pr?.icon, pr?.name || "Custom"), el("b", "", pr ? pr.name : "Custom provider"));
    if (pr?.website) { const b = el("button", "link", hostOf(pr.website) + " ↗"); b.onclick = () => api("open", { url: pr.website }); h.append(b); }
    ed.append(h);
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
  eye.onclick = () => { key.type = key.type === "password" ? "text" : "password"; eye.textContent = key.type === "password" ? "Show" : "Hide"; };
  side.append(eye);
  const keysUrl = p?.keysUrl || pr?.keysUrl;
  if (keysUrl) { const b = el("button", "link", "Get a key ↗"); b.onclick = () => api("open", { url: keysUrl }); side.append(b); }
  const keyWrap = el("div", "pair");
  keyWrap.append(key, side);
  ed.append(...field("API key", keyWrap, isNew ? "Kept in ~/.config/dial/providers.json (0600). Nothing is read from your shell." : ""));

  if (p) ed.append(...field("Models", renderModels(p), ""));
  else if (custom) {
    const ex = input("", "model ids, comma separated · e.g. gpt-5.5, claude-sonnet-5");
    ex.oninput = () => { draft.extra = ex.value.split(/[,\s]+/).filter(Boolean); };
    ed.append(...field("Models", ex, "Optional: dial asks the vendor for its list after saving."));
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
  const results = el("div", "results");
  if (p) {
    const test = el("button", "text", "Test");
    test.title = "Send a tiny request through each endpoint";
    test.onclick = async () => {
      test.classList.add("busy");
      results.replaceChildren(el("span", "res", "testing…"));
      try {
        const r = await api("provider/test", { id: p.id });
        results.replaceChildren();
        for (const t of r.results) {
          const b = el("span", "res " + (t.ok ? "ok" : "bad"));
          b.append(el("span", "", `${t.protocol} ${t.ok ? "✓" : "✗"}`));
          if (t.ok) b.append(el("span", "ms", `${t.ms} ms`));
          else b.append(el("span", "", t.status ? `${t.status} · ${t.error}` : t.error));
          b.title = t.model ? `model ${t.model}` : "";
          results.append(b);
        }
      } catch (e) { results.replaceChildren(); status(e.message, "err"); }
      test.classList.remove("busy");
    };
    bar.append(test, results);
    bar.append(el("span", "grow"));
    const del = el("button", "text danger", "Remove");
    del.onclick = () => providerAction("delete", { id: p.id }, `${p.name} removed`);
    bar.append(del);
  } else bar.append(el("span", "grow"));
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

// ---------- header / footer ----------

function show(v) {
  view = v;
  for (const b of $("#nav").children) b.classList.toggle("on", b.dataset.view === v);
  $("#view-agents").hidden = v !== "agents";
  $("#view-providers").hidden = v !== "providers";
  closePicker();
  if (v === "providers") loadProviders().catch((e) => status(e.message, "err"));
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
load();
