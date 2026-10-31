// magpie — one state object per view, rendered into a list. No framework.
const $ = (s) => document.querySelector(s);
const $$ = (s) => document.querySelectorAll(s);
const params = new URLSearchParams(location.search);
const mode = params.get("mode") || "window";
document.body.classList.add(mode);
// Only the Mac window draws its title bar inside the page (the traffic lights).
if (/^Mac/.test(navigator.platform)) document.body.classList.add("mac");
if (params.get("theme")) document.documentElement.dataset.theme = params.get("theme");

let state = { agents: [], profiles: [], catalog: "", settings: {} };
let prefs = null; // the settings page: theme, lang, version, dir, gateway
let providers = null; // { providers, presets, gateway }
let view = "agents";
let period = "30d"; // usage window
let usage = null;   // last usage summary
let pick = null; // { agent, field, options, items, cursor, anchor }
let editing = null; // provider id being edited; { preset } or { custom: true } for a new one
let draft = null; // the editor's working copy
let adding = false; // the preset sheet is open
// the gateway tab's choices, kept per machine
let flavor = params.get("flavor") || localStorage.getItem("magpie.flavor") || "openai"; // which API the snippets speak
let lang = params.get("lang") || localStorage.getItem("magpie.lang") || "shell";        // which snippet
let exampleModel = localStorage.getItem("magpie.model") || "";  // the model in the snippets
const expandedCalls = new Set(); // recent-call ids whose wire bodies are open
let savedModelFavorites = [];
try { savedModelFavorites = JSON.parse(localStorage.getItem("magpie.modelFavorites") || "[]"); } catch {}
const modelFavorites = new Set(Array.isArray(savedModelFavorites) ? savedModelFavorites : []);

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
const COPY_ICON = "M5.5 5.5V3.5h7v7h-2M3.5 5.5h7v7h-7z";

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
    e.append(el("b", "", t("No agents found")), el("span", "", t("Install Claude Code, Codex, Gemini CLI, OpenCode… and magpie will list them here.")));
    list.append(e);
  }
  for (const a of state.agents) {
    const row = el("div", "row agent");
    row.dataset.id = a.id;
    row.title = a.path;
    const who = el("div", "who");
    who.append(el("div", "name", a.name));
    // the model picker takes the wide column, everything else the narrow one,
    // so the controls line up down the list
    const fields = el("div", "fields");
    const wide = (f) => f.label === "model" || f.label === "large";
    const sorted = [...a.fields].sort((x, y) => wide(y) - wide(x));
    for (const f of sorted) {
      const b = el("button", "field " + (sorted.length === 1 ? "solo" : wide(f) ? "main" : "side"));
      const opt = optionFor(f, f.value);
      b.title = t("{label}: {value}", { label: t(f.label), value: f.value || t("agent default") }) + (opt?.note ? ` · ${opt.note}` : "");
      if (opt?.icon) b.append(icon(opt.icon));
      else if (!wide(f) || !f.value) b.append(el("span", "k", t(f.label)));
      const shown = (f.key === "effort" || f.label === "effort" || f.label === "thinking") ? effortName(opt || { value: f.value }) : (opt?.label || f.value || t("default"));
      b.append(el("span", "v" + (f.value ? "" : " empty"), shown));
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
  if (!state.profiles.length) chips.append(el("span", "hint", t("none yet · save the setup to switch back in one click")));
  for (const p of state.profiles) {
    const c = el("button", "chip");
    c.title = p.summary;
    c.append(el("span", "", p.name));
    const x = el("span", "x", "×");
    x.title = t("Delete profile");
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
    applyPrefs(state.settings);
    renderAgents();
    if (view === "providers" || view === "gateway") await loadProviders();
    if (view === "usage") await loadUsage();
    if (view === "settings") await loadSettings();
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
  const effortPicker = !only && (field.key === "effort" || field.label === "effort" || field.label === "thinking");
  // Current model first, then the rest in catalog order. Effort levels keep
  // their natural low → high order because their position is meaningful.
  const i = options.findIndex((o) => o.value === cur);
  if (!effortPicker && i > 0) { const [c] = options.splice(i, 1); options.unshift({ ...c, group: "" }); }
  else if (i < 0 && cur && !only) options.unshift({ value: cur, note: t("current value") });
  // the agent's own default: magpie's wiring comes out and the key is removed
  if (!only) options.unshift({ value: "", label: t("Default"), note: t("what {agent} ships with", { agent: agent.name }), icon: agent.icon, reset: true });
  const modelPicker = ["model", "small", "large"].includes(field.label) && !only;
  pick = { agent, field, options, anchor, cursor: 0, free: !only, modelPicker, effortPicker, groupFilter: "all" };
  anchor.classList.add("open");
  const pop = $("#pop");
  pop.classList.toggle("model-picker", modelPicker);
  pop.classList.toggle("effort-picker", effortPicker);
  pop.hidden = false;
  $("#effortControl").hidden = !effortPicker;
  pop.querySelector(".search").hidden = effortPicker;
  pop.querySelector(".picker-body").hidden = effortPicker;
  placePop(anchor, effortPicker ? 218 : modelPicker ? Math.min(490, innerWidth - 16) : (options.some((o) => o.note && o.note !== o.value) ? 372 : 300), effortPicker ? 90 : modelPicker ? Math.min(420, innerHeight - 16) : 340);
  if (effortPicker) {
    renderEffortPicker();
    $("#effortRange").focus();
    return;
  }
  const q = $("#q");
  q.value = "";
  q.placeholder = modelPicker ? t("Filter, or type any model id…") : t("Filter {field}…", { field: t(field.label) });
  filter();
  q.focus();
}

function effortName(option) {
  if (!option?.value) return t("default");
  return t(option.label || option.value);
}

function renderEffortPicker() {
  const range = $("#effortRange");
  const options = pick.options;
  const selected = Math.max(0, options.findIndex((o) => o.value === pick.field.value));
  range.max = String(Math.max(0, options.length - 1));
  range.value = String(selected);
  $("#effortTitle").textContent = t(pick.field.label);
  const update = () => {
    const i = Number(range.value);
    $("#effortValue").textContent = effortName(options[i]);
    const fill = `${options.length > 1 ? 100 * i / (options.length - 1) : 0}%`;
    range.style.setProperty("--fill", fill);
    range.closest(".effort-track").style.setProperty("--fill", fill);
    range.setAttribute("aria-valuetext", effortName(options[i]));
  };
  range.oninput = update;
  range.onchange = () => {
    const opened = pick;
    const option = options[Number(range.value)];
    if (!opened || !option || option.value === opened.field.value) return;
    opened.field.value = option.value;
    const value = opened.anchor.querySelector(".v");
    if (value) {
      value.textContent = effortName(option);
      value.classList.toggle("empty", !option.value);
    }
    // Persist every settled slider value, but keep the compact control open so
    // the user can compare adjacent levels. Queue writes to preserve ordering
    // when keyboard input changes several stops quickly.
    opened.effortSave = (opened.effortSave || Promise.resolve()).then(async () => {
      const next = await api("set", { agent: opened.agent.id, field: opened.field.key, value: option.value });
      state = next;
      status(`${opened.agent.name} ${t(opened.field.label)} → ${effortName(option)}`, "ok");
    }).catch((e) => status(e.message, "err"));
  };
  range.onkeydown = (ev) => {
    if (ev.key === "Escape") { ev.preventDefault(); ev.stopPropagation(); closePicker(); }
  };
  update();
}

function filter() {
  if (!pick) return;
  const q = $("#q").value.trim().toLowerCase();
  let source = pick.options;
  if (pick.modelPicker && pick.groupFilter === "favorites") source = source.filter((o) => modelFavorites.has(o.value));
  else if (pick.modelPicker && pick.groupFilter !== "all") source = source.filter((o) => o.group === pick.groupFilter || o.reset);
  const scored = source.map((o) => ({ o, i: pick.options.indexOf(o), s: score(q, o) })).filter((x) => x.s > 0);
  // with a query, best matches first; without, catalog order keeps the groups together
  if (q) scored.sort((a, b) => b.s - a.s || a.i - b.i);
  pick.items = scored.map((x) => x.o);
  const typed = $("#q").value.trim();
  if (typed && pick.free && ["model", "small", "large"].includes(pick.field.label) && !pick.items.some((o) => o.value === typed)) {
    pick.items.push({ value: typed, note: t("use as typed"), custom: true });
  }
  pick.cursor = 0;
  renderPickerRail();
  renderList();
}

function updatePickerRailSelection() {
  const rail = $("#pickerRail");
  const active = rail.querySelector(`.rail-item[data-group="${CSS.escape(pick?.groupFilter || "all")}"]`);
  for (const b of rail.querySelectorAll(".rail-item")) b.classList.toggle("on", b === active);
  const thumb = rail.querySelector(".rail-thumb");
  if (active && thumb) {
    thumb.style.opacity = "1";
    thumb.style.transform = `translate3d(0, ${active.offsetTop}px, 0)`;
  }
}

function switchPickerGroup(id) {
  if (!pick?.modelPicker || id === pick.groupFilter) return;
  const railItems = [...$("#pickerRail").querySelectorAll(".rail-item")];
  const from = railItems.findIndex((b) => b.dataset.group === pick.groupFilter);
  const to = railItems.findIndex((b) => b.dataset.group === id);
  pick.groupFilter = id;
  updatePickerRailSelection();
  $("#q").focus();

  const list = $("#list");
  pick.groupAnimation?.cancel();
  const reduced = matchMedia("(prefers-reduced-motion: reduce)").matches;
  if (reduced) { filter(); return; }
  const direction = to >= from ? 1 : -1;
  const token = (pick.groupTransition || 0) + 1;
  pick.groupTransition = token;
  const out = list.animate([
    { opacity: 1, transform: "translate3d(0, 0, 0)" },
    { opacity: 0, transform: `translate3d(${-direction * 5}px, 0, 0)` },
  ], { duration: 75, easing: "cubic-bezier(.4, 0, 1, 1)", fill: "forwards" });
  pick.groupAnimation = out;
  out.finished.then(() => {
    if (!pick || pick.groupTransition !== token) return;
    out.cancel();
    filter();
    const incoming = list.animate([
      { opacity: 0, transform: `translate3d(${direction * 7}px, 0, 0)` },
      { opacity: 1, transform: "translate3d(0, 0, 0)" },
    ], { duration: 190, easing: "cubic-bezier(.22, 1, .36, 1)" });
    pick.groupAnimation = incoming;
  }).catch(() => {});
}

function renderPickerRail() {
  const rail = $("#pickerRail");
  rail.hidden = !pick?.modelPicker;
  if (!pick?.modelPicker) { rail.replaceChildren(); rail.dataset.signature = ""; return; }
  const groups = [];
  for (const o of pick.options) if (o.group && !groups.includes(o.group)) groups.push(o.group);
  const signature = groups.join("\u001f");
  if (rail.dataset.signature !== signature) {
    rail.replaceChildren();
    rail.dataset.signature = signature;
    rail.append(el("span", "rail-thumb"));
    const add = (id, title, child) => {
      const b = el("button", "rail-item");
      b.dataset.group = id;
      b.title = title;
      b.setAttribute("aria-label", title);
      b.append(child);
      b.onclick = () => switchPickerGroup(id);
      rail.append(b);
    };
    add("all", t("All models"), svg("M3 3h4v4H3zM9 3h4v4H9zM3 9h4v4H3zM9 9h4v4H9z", 15, 1.4));
    add("favorites", t("Favorites"), svg("m8 2 1.8 3.7 4.1.6-3 2.9.7 4.1L8 11.4l-3.6 1.9.7-4.1-3-2.9 4.1-.6z", 16, 1.4));
    if (groups.length) rail.append(el("span", "rail-sep"));
    for (const group of groups) {
      const sample = pick.options.find((o) => o.group === group);
      add(group, group, icon(sample?.icon || "generic"));
    }
  }
  queueMicrotask(updatePickerRailSelection);
}

function renderList() {
  const list = $("#list");
  list.replaceChildren();
  if (!pick.items.length) { list.append(el("div", "none", t("No matches."))); return; }
  const hasIcons = pick.items.some((o) => o.icon);
  const q = $("#q").value.trim();
  let group = null;
  pick.items.forEach((o, idx) => {
    if (!q && o.group && o.group !== group) list.append(el("li", "group", o.group));
    if (!q) group = o.group ?? group;
    const li = el("li", (idx === pick.cursor ? "sel" : "") + (o.value === pick.field.value ? " cur" : "") + (o.custom ? " custom" : "") + (o.reset ? " reset" : ""));
    li.dataset.i = idx;
    if (hasIcons) li.append(icon(o.icon));
    const words = el("span", "option-words");
    words.append(el("span", "v", o.label || o.value));
    let note = o.note && o.note !== (o.label || o.value) ? o.note : "";
    if (q && o.group && !note) note = o.group;
    if (note) words.append(el("span", "n", note));
    li.append(words);
    if (pick.modelPicker && o.value && !o.custom) {
      const star = el("button", "favorite" + (modelFavorites.has(o.value) ? " on" : ""));
      star.title = modelFavorites.has(o.value) ? t("Remove from favorites") : t("Add to favorites");
      star.append(svg("m8 2 1.8 3.7 4.1.6-3 2.9.7 4.1L8 11.4l-3.6 1.9.7-4.1-3-2.9 4.1-.6z", 14, 1.4));
      star.onclick = (ev) => {
        ev.stopPropagation();
        if (modelFavorites.has(o.value)) modelFavorites.delete(o.value); else modelFavorites.add(o.value);
        localStorage.setItem("magpie.modelFavorites", JSON.stringify([...modelFavorites]));
        filter();
      };
      li.append(star);
    }
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
    else status(`${agent.name} ${t(field.label)} → ${shown}`, "ok");
    if (providers) loadProviders();
  } catch (e) {
    status(e.message, "err");
  }
}

function closePicker() {
  if (!pick) return;
  pick.groupAnimation?.cancel();
  pick.anchor.classList.remove("open");
  $("#pop").hidden = true;
  $("#pop").classList.remove("model-picker", "effort-picker");
  $("#pop .search").hidden = false;
  $("#pop .picker-body").hidden = false;
  $("#effortControl").hidden = true;
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
  if (e.key !== "Escape" || pick) return;
  if (editing !== null) cancelEdit();
  else if (mode === "panel") api("window/hide", {});
});

// ---------- profiles ----------

async function profileAction(action, name) {
  try {
    const data = await api("profile/" + action, { name });
    state = data;
    renderAgents();
    if (action === "use") status(t(data.changed === 1 ? "{name} applied · {n} setting changed" : "{name} applied · {n} settings changed", { name, n: data.changed }), "ok");
    else if (action === "save") status(t("Saved {name}", { name }), "ok");
    else status(t("Deleted {name}", { name }));
  } catch (e) {
    status(e.message, "err");
  }
}

$("#save").onclick = () => {
  const chips = $("#profiles");
  if (chips.querySelector(".chip-input")) return;
  const input = el("input", "chip-input");
  input.placeholder = t("Profile name");
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
  if (view === "gateway") renderGatewayLoading();
  providers = await api("providers");
  if (!providers.providers.length && editing === null) adding = true;
  if (view === "gateway") renderGatewayView();
  else renderProviders();
}

// One row per provider: logo, name, the agents pointed at it, key status.
// Everything else lives in the editor, a dialog over the page.
function renderProviders() {
  const list = $("#providers");
  list.replaceChildren();
  list.hidden = !providers.providers.length;
  let dialog = null; // the editor, if one is open
  for (const p of providers.providers) {
    const open = editing === p.id;
    const row = el("div", "row provider" + (open ? " selected" : ""));
    row.dataset.id = p.id;
    const who = el("div", "who");
    const name = el("div", "name", p.name);
    if (p.sponsored) name.append(el("span", "badge", t("sponsored")));
    const n = p.models.filter((m) => m.on).length;
    const models = n ? t(n === 1 ? "{n} model" : "{n} models", { n }) : t("no models exposed");
    who.append(name, el("div", "sub", (p.account ? t("signed in as {user}", { user: p.account.user }) : p.host) + " · " + models));
    const using = p.agents.filter((a) => a.current);
    const uses = el("div", "uses");
    for (const a of using) {
      const b = el("button", "use");
      b.title = t("{name} · {model} — click to change", { name: a.name, model: a.model });
      b.append(icon(a.icon));
      b.onclick = (ev) => pickForAgent(a, p, b, ev);
      uses.append(b);
    }
    let key;
    if (p.account) {
      key = el("span", "key acct", accountPlan(p.account));
      key.title = t("{agent} is signed in; its models are here for every other agent", { agent: p.account.agentName });
    } else {
      key = el("span", "key " + (p.key.set ? "on" : p.ready ? "free" : "none"), p.key.set ? p.key.masked : p.ready ? t("no key") : t("needs a key"));
      key.title = p.key.set ? t("API key {masked}", { masked: p.key.masked }) : p.ready ? t("Local servers need no key") : t("Open the row and paste an API key");
    }
    const chev = el("span", "chev");
    chev.append(svg(CHEV_R, 11, 1.7));
    row.append(icon(p.icon || "generic"), who, uses, key, chev);
    row.onclick = () => { editing = open ? null : p.id; draft = null; adding = false; renderProviders(); };
    list.append(row);
    if (open) dialog = renderEditor(p);
  }
  renderExcluded();
  dialog = renderAdd() || dialog;
  if (dialog) openModal(dialog); else closeModal();
}

// Sign-ins magpie found but leaves alone, so nobody wonders why an agent that
// is clearly logged in is not in the list. Empty today.
function renderExcluded() {
  const box = $("#excluded");
  box.replaceChildren();
  for (const x of providers.excluded) {
    const r = el("div", "excluded");
    r.append(icon(x.agentIcon), el("span", "", ""));
    r.lastChild.append(el("b", "", t("{agent} is signed in, but stays out of this list. ", { agent: x.agentName })), x.why);
    box.append(r);
  }
}

// accountPlan names a signed-in account's subscription: "ChatGPT Pro", "GitHub".
function accountPlan(a) {
  if (a.agent === "codex") return "ChatGPT" + (a.plan ? " " + a.plan[0].toUpperCase() + a.plan.slice(1) : "");
  if (a.agent === "copilot") return "GitHub";
  if (a.agent === "claude") return "Claude" + (a.plan ? " " + a.plan[0].toUpperCase() + a.plan.slice(1) : "");
  return t("signed in");
}

// ---------- gateway view ----------
//
// The gateway is one local endpoint speaking four APIs; this tab is the
// page that gets anything else connected to it: base URL, key, model ids,
// and a snippet in whichever language the reader is holding.

async function copy(text, what, btn) {
  try { await navigator.clipboard.writeText(text); status(t("{what} copied", { what }), "ok"); flashCopied(btn); }
  catch { status(text); }
}

// flashCopied answers on the button itself: the icon becomes a tick and
// the button takes a green breath, then it reverts on its own. The footer
// toast says what was copied; this says it landed. Clicking again restarts
// the pop and re-arms the timer.
function flashCopied(b) {
  if (!b) return;
  b.classList.remove("done");
  void b.offsetWidth; // restart the animation when clicked again
  b.classList.add("done");
  b.title = t("Copied");
  b.replaceChildren(svg(CHECK, 12, 1.7));
  clearTimeout(b.copiedT);
  b.copiedT = setTimeout(() => {
    b.classList.remove("done");
    b.title = t("Copy");
    b.replaceChildren(svg(COPY_ICON, 12, 1.5));
  }, 1200);
}

function copyBtn(text, what) {
  const b = el("button", "copy");
  b.title = t("Copy");
  b.append(svg(COPY_ICON, 12, 1.5));
  b.onclick = (ev) => { ev.stopPropagation(); copy(text, what, b); };
  return b;
}

function renderGatewayLoading() {
  const page = $("#view-gateway");
  page.classList.add("loading");
  page.setAttribute("aria-busy", "true");
  $("#connectNote").textContent = "";
  $("#callsNote").textContent = "";
  $("#copyModels").hidden = true;

  const gateway = $("#gateway");
  gateway.replaceChildren();
  const mark = el("span", "skeleton gw-sk-dot");
  const who = el("div", "who gw-sk-who");
  who.append(el("span", "skeleton gw-sk-title"), el("span", "skeleton gw-sk-sub"));
  gateway.append(mark, who, el("span", "skeleton gw-sk-url"));

  const connect = $("#connect");
  connect.replaceChildren();
  for (let i = 0; i < 4; i++) {
    connect.append(el("span", "skeleton gw-sk-label"));
    const value = el("div", "gw-sk-field");
    value.append(el("span", "skeleton"), el("span", "skeleton short"));
    connect.append(value);
  }

  const models = $("#gwModels");
  models.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "row model gw-sk-row");
    row.append(el("span", "skeleton gw-sk-model"), el("span", "grow"), el("span", "skeleton gw-sk-provider"));
    models.append(row);
  }

  const activity = $("#activity");
  activity.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const row = el("div", "call gw-sk-call");
    row.append(el("span", "skeleton time"), el("span", "skeleton agent"), el("span", "skeleton model"), el("span", "grow"), el("span", "skeleton result"));
    activity.append(row);
  }
}

function renderGatewayView() {
  const page = $("#view-gateway");
  page.classList.remove("loading");
  page.removeAttribute("aria-busy");
  renderGateway();
  renderConnect();
  renderGatewayModels();
  renderActivity();
}

// The status card: dot, state, the URL.
function renderGateway() {
  const g = providers.gateway;
  const box = $("#gateway");
  box.replaceChildren();
  const dot = el("span", "dot " + (g.running ? "on" : ""));
  const who = el("div", "who");
  const name = el("div", "name", t("Gateway"));
  name.append(el("span", "state", t(g.running ? (g.mine ? "running" : "running · served by another magpie") : "not running")));
  const routed = new Set();
  for (const p of providers.providers) for (const a of p.agents) if (a.current) routed.add(a.id);
  const n = routed.size;
  who.append(name, el("div", "sub", g.running
    ? [t(g.models === 1 ? "{n} model" : "{n} models", { n: g.models }), n ? t(n === 1 ? "{n} agent routed through it" : "{n} agents routed through it", { n }) : t("no agent routed through it yet"), t("four APIs, one URL")].join(" · ")
    : t("start it with magpie serve, or open magpie at login")));
  const url = el("button", "url");
  url.append(el("code", "", g.url));
  url.title = t("Copy the gateway URL");
  url.onclick = () => copy(g.url, t("Gateway URL"));
  box.append(dot, who, url);
}

// One entry per API the gateway serves: where each SDK's base URL points,
// the env vars the usual tools read, and a request in four dialects.
const FLAVORS = {
  openai: {
    name: "OpenAI", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "Chat Completions, the API most tools speak. Anything with an OpenAI base-URL setting works.",
    curl: (b, m) => `curl ${b}/chat/completions \\
  -H "Authorization: Bearer magpie" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${m}",
       "messages": [{"role": "user", "content": "hi"}]}'`,
    python: (b, m) => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="magpie")
r = client.chat.completions.create(
    model="${m}",
    messages=[{"role": "user", "content": "hi"}],
)
print(r.choices[0].message.content)`,
    node: (b, m) => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "magpie" });
const r = await client.chat.completions.create({
  model: "${m}",
  messages: [{ role: "user", content: "hi" }],
});
console.log(r.choices[0].message.content);`,
  },
  responses: {
    name: "Responses", base: (u) => u + "/v1", baseEnv: "OPENAI_BASE_URL", keyEnv: "OPENAI_API_KEY",
    note: "OpenAI's newer API: reasoning, built-in tool items, encrypted reasoning. Codex speaks this.",
    curl: (b, m) => `curl ${b}/responses \\
  -H "Authorization: Bearer magpie" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${m}", "input": "hi"}'`,
    python: (b, m) => `from openai import OpenAI

client = OpenAI(base_url="${b}", api_key="magpie")
r = client.responses.create(model="${m}", input="hi")
print(r.output_text)`,
    node: (b, m) => `import OpenAI from "openai";

const client = new OpenAI({ baseURL: "${b}", apiKey: "magpie" });
const r = await client.responses.create({ model: "${m}", input: "hi" });
console.log(r.output_text);`,
  },
  anthropic: {
    name: "Anthropic", base: (u) => u, baseEnv: "ANTHROPIC_BASE_URL", keyEnv: "ANTHROPIC_API_KEY",
    note: "Messages API. Claude Code reads ANTHROPIC_AUTH_TOKEN instead of the key; the Agents tab sets that for you.",
    curl: (b, m) => `curl ${b}/v1/messages \\
  -H "x-api-key: magpie" \\
  -H "anthropic-version: 2023-06-01" \\
  -H "Content-Type: application/json" \\
  -d '{"model": "${m}", "max_tokens": 1024,
       "messages": [{"role": "user", "content": "hi"}]}'`,
    python: (b, m) => `import anthropic

client = anthropic.Anthropic(
    base_url="${b}", api_key="magpie",
)
m = client.messages.create(
    model="${m}",
    max_tokens=1024,
    messages=[{"role": "user", "content": "hi"}],
)
print(m.content[0].text)`,
    node: (b, m) => `import Anthropic from "@anthropic-ai/sdk";

const client = new Anthropic({ baseURL: "${b}", apiKey: "magpie" });
const m = await client.messages.create({
  model: "${m}",
  max_tokens: 1024,
  messages: [{ role: "user", content: "hi" }],
});
console.log(m.content[0].text);`,
  },
  gemini: {
    name: "Gemini", base: (u) => u, baseEnv: "GOOGLE_GEMINI_BASE_URL", keyEnv: "GEMINI_API_KEY",
    note: "Google's generateContent API, v1beta. Gemini CLI and the google-genai SDKs speak this.",
    curl: (b, m) => `curl ${b}/v1beta/models/${m}:generateContent \\
  -H "x-goog-api-key: magpie" \\
  -H "Content-Type: application/json" \\
  -d '{"contents": [{"parts": [{"text": "hi"}]}]}'`,
    python: (b, m) => `from google import genai

client = genai.Client(api_key="magpie", http_options={"base_url": "${b}"})
r = client.models.generate_content(model="${m}", contents="hi")
print(r.text)`,
    node: (b, m) => `import { GoogleGenAI } from "@google/genai";

const ai = new GoogleGenAI({
  apiKey: "magpie",
  httpOptions: { baseUrl: "${b}" },
});
const r = await ai.models.generateContent({ model: "${m}", contents: "hi" });
console.log(r.text);`,
  },
};
const LANGS = [["shell", "Shell"], ["curl", "curl"], ["python", "Python"], ["node", "Node"]];

// every exposed model, as the ids agents use
function gatewayModels() {
  const out = [];
  for (const p of providers.providers) for (const m of p.models) if (m.on) out.push({ id: `${p.id}/${m.id}`, name: m.name, provider: p });
  return out;
}

function segs(items, current, onPick) {
  const box = el("div", "segs");
  const key = items.map(([id]) => id).join("|");
  for (const [id, name] of items) {
    const b = el("button", "opt" + (id === current ? " on" : ""), name);
    b.onclick = () => { for (const x of box.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(box, key); onPick(id); };
    box.append(b);
  }
  queueMicrotask(() => slide(box, key)); // once it is in the page
  return box;
}

function renderConnect() {
  const g = providers.gateway;
  const box = $("#connect");
  box.replaceChildren();
  const models = gatewayModels();
  if (!models.some((m) => m.id === exampleModel)) exampleModel = models[0]?.id || "";
  const model = exampleModel || "provider/model";
  const f = FLAVORS[flavor] || FLAVORS.openai;
  const base = f.base(g.url);
  $("#connectNote").textContent = t("Loopback only · the key can be anything");

  box.append(...field("API", segs(Object.entries(FLAVORS).map(([k, v]) => [k, v.name]), flavor, (id) => { flavor = id; localStorage.setItem("magpie.flavor", id); renderConnect(); }), t(f.note)));

  const b = el("div", "val");
  b.append(el("code", "", base), copyBtn(base, "Base URL"));
  box.append(...field("Base URL", b, t("What {env} takes.", { env: f.baseEnv })));

  const k = el("div", "val");
  k.append(el("code", "", "magpie"), copyBtn("magpie", t("Key")));
  box.append(...field(t("API key"), k, t("{env}=magpie. The gateway trusts everything on loopback, so any value works.", { env: f.keyEnv })));

  const m = el("div", "val");
  m.append(el("code", "", model), copyBtn(model, t("Model id")));
  box.append(...field(t("Model"), m, t(models.length ? "provider/model, as listed below. Click a model there to put it in the snippets." : "No models yet. Add a provider, or sign in to Codex or Copilot.")));

  const ex = el("div", "stack");
  ex.append(segs(LANGS, lang, (id) => { lang = id; localStorage.setItem("magpie.lang", id); renderConnect(); }));
  const code = lang === "shell"
    ? `export ${f.baseEnv}=${base}\nexport ${f.keyEnv}=magpie`
    : f[lang](base, model);
  const pre = el("pre", "snip");
  const c = el("code");
  c.append(highlight(code, lang));
  pre.append(c, copyBtn(code, t("Snippet")));
  ex.append(pre);
  box.append(...field(t("Example"), ex, lang === "shell" ? t("Put these in the shell (or the tool's settings) and the tool talks to magpie instead of the vendor.") : ""));
}

// A small highlighter for the four snippet dialects: strings, comments,
// keywords, numbers, calls, and the env vars and flags shells care about.
const KEYWORDS = {
  python: /^(from|import|def|return|await|async|for|in|if|else|None|True|False)$/,
  node: /^(import|from|const|let|await|async|new|return|function|export|default)$/,
  shell: /^(export|curl)$/,
  curl: /^(curl)$/,
};
function highlight(code, lang) {
  const re = lang === "node"
    ? /("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|`(?:[^`\\]|\\.)*`)|(\/\/.*)|(\b\d+(?:\.\d+)?\b)|([A-Za-z_$][\w$]*)(?=\s*\()|([A-Za-z_$][\w$]*)|(\s+|.)/g
    : /("(?:[^"\\]|\\.)*"|'[^']*'|(?<==)\S+)|(#.*)|(\b\d+(?:\.\d+)?\b(?=[,\s\]}]))|(-{1,2}[A-Za-z][\w-]*)|([A-Z][A-Z0-9_]+)(?==)|([A-Za-z_][\w.]*)(?=\s*\()|([A-Za-z_][\w.]*)|(\\\n)|(\s+|.)/g;
  const out = document.createDocumentFragment();
  const kw = KEYWORDS[lang] || KEYWORDS.shell;
  let m;
  while ((m = re.exec(code))) {
    let cls = "";
    if (lang === "node") {
      if (m[1]) cls = "s"; else if (m[2]) cls = "c"; else if (m[3]) cls = "n";
      else if (m[4]) cls = kw.test(m[4]) ? "k" : "f"; else if (m[5] && kw.test(m[5])) cls = "k";
    } else {
      if (m[1]) cls = "s"; else if (m[2]) cls = "c"; else if (m[3]) cls = "n"; else if (m[4]) cls = "o";
      else if (m[5]) cls = "v"; else if (m[6]) cls = kw.test(m[6]) ? "k" : "f"; else if (m[7] && kw.test(m[7])) cls = "k";
      else if (m[9]) cls = "o";
    }
    if (cls) out.append(el("span", "tk-" + cls, m[0]));
    else out.append(m[0]);
  }
  return out;
}

function renderGatewayModels() {
  const list = $("#gwModels");
  list.replaceChildren();
  const models = gatewayModels();
  $("#copyModels").hidden = !models.length;
  $("#copyModels").onclick = () => copy(models.map((m) => m.id).join("\n"), t("Model ids"));
  if (!models.length) {
    list.append(el("div", "empty-state", "")).append(el("b", "", t("No models exposed yet")), t("Add a provider, or sign in to Codex or Copilot; their models show up here for every agent."));
    return;
  }
  for (const m of models) {
    const row = el("div", "row model" + (m.id === exampleModel ? " selected" : ""));
    const who = el("div", "who");
    who.append(el("div", "name", m.id), el("div", "sub", m.name && m.name !== m.id.split("/")[1] ? `${m.name} · ${m.provider.name}` : m.provider.name));
    row.append(icon(m.provider.icon || "generic"), who, copyBtn(m.id, t("Model id")));
    row.title = t("Use this model in the snippets");
    row.onclick = () => { exampleModel = m.id; localStorage.setItem("magpie.model", m.id); renderConnect(); renderGatewayModels(); };
    list.append(row);
  }
}

function formatWireBody(raw) {
  if (!raw) return "";
  try { return JSON.stringify(JSON.parse(raw), null, 2); } catch { return raw; }
}

function callBodyPanel(label, raw, truncated) {
  const panel = el("section", "call-body");
  const head = el("div", "call-body-head");
  head.append(el("span", "call-body-label", t(label)));
  if (truncated) head.append(el("span", "call-body-truncated", t("first 256 KB")));
  const formatted = formatWireBody(raw);
  if (formatted) head.append(el("span", "grow"), copyBtn(raw, t(label)));
  panel.append(head);
  const pre = el("pre");
  const code = el("code", "", formatted || t("No body captured"));
  if (!formatted) code.classList.add("empty");
  pre.append(code);
  panel.append(pre);
  return panel;
}

function renderActivity() {
  const g = providers.gateway;
  const box = $("#activity");
  box.replaceChildren();
  $("#callsNote").textContent = g.running && !g.mine ? t("shown by the magpie that serves the gateway") : "";
  const calls = g.calls.slice(0, 20);
  if (!calls.length) { box.append(el("div", "none", t("No requests yet. Point an agent at a model, or run the example above; every call shows up here as it happens."))); return; }
  for (const c of calls) {
    const id = `${c.time}|${c.agent}|${c.model}`;
    const open = expandedCalls.has(id);
    const item = el("div", "call-item" + (open ? " open" : "") + (c.status >= 400 ? " bad" : ""));
    const r = el("div", "call");
    r.setAttribute("role", "button");
    r.setAttribute("tabindex", "0");
    r.setAttribute("aria-expanded", String(open));
    const chev = el("span", "call-chev");
    chev.append(svg(CHEV_R, 11, 1.6));
    r.append(chev);
    r.append(el("span", "when", new Date(c.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" })));
    r.append(el("span", "a", c.agent || "—"));
    r.append(el("span", "m", c.model));
    r.append(el("span", "p", c.from === c.to ? c.from : `${c.from} → ${c.to}`));
    r.append(el("span", "grow"));
    r.append(el("span", "st", c.error ? `${c.status} ${c.error}` : `${c.status} · ${c.ms} ms`));
    r.title = open ? t("Hide request and response bodies") : t("Show request and response bodies");
    const toggle = () => {
      if (expandedCalls.has(id)) expandedCalls.delete(id); else expandedCalls.add(id);
      renderActivity();
    };
    r.onclick = toggle;
    r.onkeydown = (ev) => {
      if (ev.key === "Enter" || ev.key === " ") { ev.preventDefault(); toggle(); }
    };
    item.append(r);
    if (open) {
      const details = el("div", "call-details");
      details.append(
        callBodyPanel("Request Body", c.requestBody, c.requestTruncated),
        callBodyPanel("Response Body", c.responseBody, c.responseTruncated),
      );
      item.append(details);
    }
    box.append(item);
  }
}

// An agent's model field, and whether any of its options come from provider p.
function modelField(a) {
  const agent = state.agents.find((x) => x.id === a.id);
  return agent?.fields.find((f) => f.key === "model") || null;
}
function ofProvider(p) { return new RegExp(`^(magpie/)?${p.id.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")}/`); }

// Pick one of this provider's models for an agent, straight from the row.
function pickForAgent(a, p, btn, ev) {
  const agent = state.agents.find((x) => x.id === a.id);
  const field = modelField(a);
  if (!field) return;
  const pre = ofProvider(p);
  if (!field.options.some((o) => pre.test(o.value))) {
    ev.stopPropagation();
    status(t("{p} exposes no models yet — pick some below", { p: p.name }), "warn");
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
  if (!adding) return null;
  const head = el("div", "row-head");
  head.append(el("span", "label", t(providers.providers.length ? "Add a provider" : "Add your first provider")), el("span", "grow"));
  const q = input(presetQuery, t("Find a vendor…"));
  q.className = "find";
  q.oninput = () => { presetQuery = q.value; drawTiles(); };
  head.append(q);
  if (providers.providers.length) {
    const x = el("button", "text", t("Close"));
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
      tiles.append(el("div", "kind", t(title)));
      const grid = el("div", "grid");
      for (const pr of ps) grid.append(tile(pr));
      if (kind === "local" && !f) {
        const c = el("button", "tile custom" + (editing?.custom ? " on" : ""));
        const ic = el("span", "ic plus");
        ic.append(svg(PLUS, 13, 1.8));
        c.append(ic, el("span", "tt"));
        c.lastChild.append(el("span", "n", t("Custom")), el("span", "s", t("any compatible URL")));
        c.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
        grid.append(c);
      }
      tiles.append(grid);
    }
    if (!any) {
      const none = el("div", "none");
      none.append(t("Nothing called “{q}”. ", { q: presetQuery.trim() }));
      const b = el("button", "link", t("Add it as a custom provider"));
      b.onclick = () => { editing = { custom: true }; draft = null; renderProviders(); };
      none.append(b);
      tiles.append(none);
    }
  };
  drawTiles();
  return editing && typeof editing === "object" ? renderEditor(null, editing.preset) : null;
}

function tile(pr) {
  const b = el("button", "tile" + (pr.added ? " added" : "") + (editing?.preset === pr.id ? " on" : ""));
  b.append(icon(pr.icon || "generic"));
  const tt = el("span", "tt");
  const n = el("span", "n", pr.name);
  if (pr.sponsored) n.append(el("span", "badge", t("sponsored")));
  tt.append(n, el("span", "s", pr.note || hostOf(pr.chat || pr.responses || pr.anthropic)));
  b.append(tt);
  if (pr.added) {
    const ck = el("span", "check");
    ck.append(svg(CHECK, 10, 2));
    b.append(ck);
    b.title = t("{name} is already added — open it", { name: pr.name });
    b.onclick = () => { editing = pr.id; adding = false; draft = null; renderProviders(); };
  } else {
    b.onclick = () => { editing = { preset: pr.id }; draft = null; renderProviders(); };
  }
  return b;
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

// ---------- modal ----------
// The provider editor opens as a dialog over the page; Escape, the backdrop
// or Cancel close it.
let modalTimer = 0;
function openModal(content) {
  const m = $("#modal"), d = m.firstElementChild;
  clearTimeout(modalTimer);
  m.classList.remove("out");
  d.classList.remove("swap");
  if (!m.hidden) { void d.offsetWidth; d.classList.add("swap"); } // content changed: a soft refresh, not a re-entrance
  d.replaceChildren(content);
  m.hidden = false;
}
function closeModal() {
  const m = $("#modal");
  if (m.hidden || m.classList.contains("out")) return;
  m.classList.add("out");
  modalTimer = setTimeout(() => { m.hidden = true; m.classList.remove("out"); m.firstElementChild.replaceChildren(); }, 170);
}
$("#modal").onclick = (e) => { if (e.target === e.currentTarget) cancelEdit(); };

// ---------- sliding thumb ----------
// Pills (the nav, every segmented control) have one thumb that glides to the
// selected option instead of each option lighting up on its own.
const thumbs = new Map(); // pill key → where its thumb is, so a re-rendered pill takes over mid-slide
function slide(box, key) {
  let th = box.querySelector(":scope > .thumb");
  if (!th) { th = el("span", "thumb"); box.prepend(th); }
  const on = box.querySelector(":scope > .on");
  if (!on) { th.style.opacity = "0"; return; }
  th.style.opacity = "";
  const to = { x: on.offsetLeft, w: on.offsetWidth };
  const last = thumbs.get(key);
  const from = last && performance.now() - last.at < 300 ? last.from : last; // re-rendered mid-slide: start where the old one started
  const put = (p) => { th.style.transform = `translateX(${p.x}px)`; th.style.width = p.w + "px"; };
  th.classList.add("still");
  put(from || to);
  void th.offsetWidth;
  th.classList.remove("still");
  put(to);
  thumbs.set(key, { ...to, at: performance.now(), from: from || to });
}

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

  {
    const h = el("div", "ehead");
    h.append(icon(p?.icon || pr?.icon || "generic"), el("b", "", p ? p.name : pr ? pr.name : t("Custom provider")));
    if (pr?.note) h.append(el("span", "note", pr.note));
    h.append(el("span", "grow"));
    const site = pr?.website || (p?.host ? "https://" + p.host : "");
    if (site) { const b = el("button", "link", hostOf(site) + " ↗"); b.onclick = () => api("open", { url: site }); h.append(b); }
    ed.append(h);
  }

  // who uses it: just the agents already pointed here, so a click changes
  // one's model. Pointing a new agent at the provider happens in the Agent
  // tab's picker, not here.
  if (p) {
    const on = p.agents.filter((a) => a.current);
    if (on.length) {
      const chips = el("div", "achips");
      for (const a of on) {
        const c = el("button", "achip on");
        c.append(icon(a.icon), el("span", "n", a.name), el("span", "m", a.model));
        c.title = t("{agent} is on {model} — click to change", { agent: a.name, model: a.model });
        c.onclick = (ev) => pickForAgent(a, p, c, ev);
        chips.append(c);
      }
      ed.append(...field(t("Agents"), chips, ""));
    }
  }

  let name, url;
  if (custom) {
    name = input(draft.name, t("e.g. My Relay"));
    name.oninput = () => { draft.name = name.value; if (isNew) draft.id = slug(name.value); };
    ed.append(...field(t("Name"), name));

    const seg = el("div", "segs");
    for (const [v, l, hint] of [["openai", "OpenAI compatible", "…/v1 — chat completions, and responses when the vendor has it"], ["anthropic", "Anthropic compatible", "the root URL, what ANTHROPIC_BASE_URL would take"]]) {
      const b = el("button", "opt" + (draft.api === v ? " on" : ""), t(l));
      b.title = t(hint);
      b.onclick = () => { draft.api = v; const u = url.value; if (v === "anthropic") { draft.anthropic = u; draft.chat = ""; } else { draft.chat = u; draft.anthropic = ""; } for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(seg, "api"); url.placeholder = v === "anthropic" ? "https://…" : "https://…/v1"; };
      seg.append(b);
    }
    queueMicrotask(() => slide(seg, "api"));
    url = input(draft.api === "anthropic" ? draft.anthropic : draft.chat, draft.api === "anthropic" ? "https://…" : "https://…/v1", "url");
    url.oninput = () => { if (draft.api === "anthropic") draft.anthropic = url.value; else draft.chat = url.value; };
    const urlWrap = el("div", "stack");
    urlWrap.append(seg, url);
    ed.append(...field("Base URL", urlWrap));
  }

  if (p?.account) {
    // the sign-in belongs to the agent; magpie only borrows it
    const a = p.account;
    const acct = el("div", "acct");
    acct.append(icon(a.agentIcon), el("span", "n", a.user), el("span", "plan", accountPlan(a)));
    ed.append(...field(t("Account"), acct, t("{agent}'s sign-in, read from its own files. Sign out there and this provider goes away.", { agent: a.agentName })));
    ed.append(...field(t("Models"), renderModels(p), ""));
    ed.append(...field(t("Endpoints"), renderEndpoints(p, p)));
    const bar = el("div", "bar");
    bar.append(el("span", "grow"));
    const cancel = el("button", "text", t("Cancel"));
    cancel.onclick = cancelEdit;
    const saveBtn = el("button", "text primary", t("Save"));
    saveBtn.onclick = () => { saveBtn.classList.add("busy"); providerAction("save", { id: p.id, models: draft.chosen }, t("{name} saved", { name: p.name })); };
    bar.append(cancel, saveBtn);
    ed.append(bar);
    return ed;
  }

  const key = input("", p?.key.set ? t("{masked} · paste a new key to replace it", { masked: p.key.masked }) : t(pr?.noKey || p?.key.optional ? "optional for local servers" : "paste an API key"), "password");
  key.oninput = () => { draft.key = key.value; };
  key.onkeydown = (e) => { e.stopPropagation(); if (e.key === "Enter" && isNew) save(); else if (e.key === "Escape") cancelEdit(); };
  const side = el("div", "side");
  const eye = el("button", "text", t("Show"));
  let revealed = false; // the saved key is in the box, not a draft
  eye.onclick = async () => {
    if (key.type === "password") {
      if (!key.value && p?.key.set) {
        try { key.value = (await api("provider/key", { id: p.id })).key; revealed = true; } catch (e) { status(e.message, "err"); return; }
      }
      key.type = "text"; eye.textContent = t("Hide");
    } else {
      if (revealed && !draft.key) key.value = "";
      revealed = false;
      key.type = "password"; eye.textContent = t("Show");
    }
  };
  side.append(eye);
  const keysUrl = p?.keysUrl || pr?.keysUrl;
  if (keysUrl) { const b = el("button", "link", t("Get a key ↗")); b.onclick = () => api("open", { url: keysUrl }); side.append(b); }
  const keyWrap = el("div", "pair");
  keyWrap.append(key, side);
  ed.append(...field(t("API key"), keyWrap, isNew ? t("Kept in ~/.config/magpie/providers.json, readable by you alone. Nothing is read from your shell.") : ""));

  // a relay that offers several regional endpoints: one selector, and the
  // provider's base URLs follow it
  let refreshEndpoints = () => {};
  if (pr?.regions?.length) {
    const seg = el("div", "segs");
    const cur = pr.regions.find((r) => r.chat && r.chat === (draft.chat || pr.chat)) || pr.regions[0];
    for (const r of pr.regions) {
      const b = el("button", "opt" + (r.id === cur.id ? " on" : ""), t(r.name));
      b.onclick = () => {
        draft.chat = r.chat || ""; draft.responses = r.responses || ""; draft.anthropic = r.anthropic || "";
        for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b);
        slide(seg, "regions");
        refreshEndpoints();
      };
      seg.append(b);
    }
    queueMicrotask(() => slide(seg, "regions"));
    ed.append(...field(t("Region"), seg, t("which endpoint {p} is reached through", { p: pr.name })));
  }

  if (p) ed.append(...field(t("Models"), renderModels(p), ""));
  else if (custom) {
    const ex = input("", t("model ids, comma separated · e.g. gpt-5.5, claude-sonnet-5"));
    ex.oninput = () => { draft.extra = ex.value.split(/[,\s]+/).filter(Boolean); };
    ed.append(...field(t("Models"), ex, t("Optional: magpie asks the vendor for its list after saving.")));
  }

  if (!custom) {
    const ebox = el("div");
    refreshEndpoints = () => {
      const base = p || pr || {};
      const src = { chat: draft.chat || base.chat || "", responses: draft.responses || base.responses || "", anthropic: draft.anthropic || base.anthropic || "" };
      ebox.replaceChildren(renderEndpoints(p, src));
    };
    refreshEndpoints();
    ed.append(...field(t("Endpoints"), ebox, ""));
  }

  if (custom) {
    const more = el("details", "more");
    more.append(el("summary", "", t("More endpoints")));
    const inner = el("div", "inner");
    const add = (label, key, ph, hint) => {
      const i = input(draft[key], ph, "url");
      i.oninput = () => { draft[key] = i.value; };
      inner.append(...field(t(label), i, t(hint)));
    };
    if (draft.api === "anthropic") add("OpenAI URL", "chat", "https://…/v1", "if the vendor also serves chat completions");
    else add("Anthropic URL", "anthropic", "https://…", "if the vendor also serves Anthropic messages");
    add("Responses URL", "responses", "https://…/v1", "if the vendor serves the OpenAI Responses API (Codex uses it natively)");
    const cat = input(draft.catalog, t("models.dev id, e.g. openai"));
    cat.oninput = () => { draft.catalog = cat.value; };
    inner.append(...field(t("Catalog"), cat, t("Display names and reasoning levels for the models")));
    more.append(inner);
    ed.append(more);
  }

  const bar = el("div", "bar");
  if (p) {
    const del = el("button", "text danger", t("Remove"));
    del.onclick = () => providerAction("delete", { id: p.id }, t("{name} removed", { name: p.name }));
    bar.append(del);
  }
  bar.append(el("span", "grow"));
  const cancel = el("button", "text", t("Cancel"));
  cancel.onclick = cancelEdit;
  const saveBtn = el("button", "text primary", t(isNew ? "Add" : "Save"));
  const save = () => {
    const body = { id: draft.id, name: draft.name, preset: draft.preset, key: draft.key || "", chat: draft.chat, responses: draft.responses, anthropic: draft.anthropic, catalog: draft.catalog, models: p ? draft.chosen : draft.extra };
    if (isNew && custom && !body.name) { name.focus(); return status(t("Give it a name"), "warn"); }
    if (isNew && custom && !body.chat && !body.anthropic) { url.focus(); return status(t("A base URL is needed"), "warn"); }
    saveBtn.classList.add("busy");
    providerAction("save", body, t(isNew ? "{name} added" : "{name} saved", { name: draft.name || draft.id }));
  };
  saveBtn.onclick = save;
  bar.append(cancel, saveBtn);
  ed.append(bar);
  setTimeout(() => (isNew ? (custom ? name : key) : null)?.focus(), 0);
  return ed;
}

// Which of the vendor's models the agents get to see: click to toggle, type
// to add one the vendor's list lacks, Refresh to ask the vendor again.
// The endpoints a provider serves, with a Test that reports against each one.
function renderEndpoints(p, src) {
  const eps = el("div", "eps");
  const slots = {};
  const urls = src || {};
  for (const [proto, label, hint] of PROTOS) {
    if (!urls[proto]) continue;
    const e = el("div", "ep");
    const pl = el("span", "pl", label);
    pl.title = t(hint);
    e.append(pl, el("code", "", urls[proto]), slots[proto] = el("span", "res"));
    eps.append(e);
  }
  if (p) {
    const test = el("button", "text action", t("Test"));
    test.title = t("Send a tiny request through each endpoint");
    test.onclick = async () => {
      test.classList.add("busy");
      for (const s of Object.values(slots)) { s.className = "res wait"; s.textContent = "…"; }
      try {
        const r = await api("provider/test", { id: p.id });
        for (const x of r.results) {
          const s = slots[x.protocol];
          if (!s) continue;
          s.className = "res " + (x.ok ? "ok" : "bad");
          s.replaceChildren();
          s.append(svg(x.ok ? CHECK : "M4.5 4.5l7 7M11.5 4.5l-7 7", 10, 2));
          s.append(el("span", "", x.ok ? `${x.ms} ms` : x.status ? `${x.status} · ${x.error}` : x.error));
          s.title = x.ok ? t("model {model}", { model: x.model }) : x.error;
        }
      } catch (e) { for (const s of Object.values(slots)) { s.className = "res"; s.textContent = ""; } status(e.message, "err"); }
      test.classList.remove("busy");
    };
    eps.append(test);
  }
  return eps;
}

function renderModels(p) {
  const box = el("div", "models");
  const chips = el("div", "mchips");
  const q = p.models.length > 24 ? input("", t("filter {n} models…", { n: p.models.length })) : null;
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
      if (++shown >= 80 && !f) { chips.append(el("span", "hint", t("… {n} more, filter to find them", { n: p.models.length - shown }))); break; }
    }
    for (const id of draft.chosen) {
      if (p.models.some((m) => m.id === id)) continue;
      const c = el("button", "mchip on own");
      c.append(el("span", "", id));
      c.title = t("Added by hand");
      c.onclick = () => { draft.chosen = draft.chosen.filter((x) => x !== id); draw(); };
      chips.append(c);
    }
    if (!p.models.length && !draft.chosen.length) chips.append(el("span", "hint", t("The vendor's list is empty. Refresh, or type a model id.")));
  };
  if (q) { q.oninput = draw; box.append(q); }
  box.append(chips);
  const foot = el("div", "mfoot");
  const add = input("", t("add a model id…"));
  add.onkeydown = (e) => {
    e.stopPropagation();
    if (e.key === "Enter" && add.value.trim()) { const id = add.value.trim(); if (!draft.chosen.includes(id)) draft.chosen.push(id); add.value = ""; draw(); }
    else if (e.key === "Escape") cancelEdit();
  };
  const refresh = el("button", "text action", t("Refresh"));
  refresh.title = t("Ask the vendor which models it serves");
  refresh.onclick = async () => {
    refresh.classList.add("busy");
    try {
      const r = await api("provider/models", { id: p.id });
      status(t("{p}: {n} models", { p: p.name, n: r.count }), "ok");
      const chosen = draft.chosen;
      await loadProviders();
      draft = draft || {};
      draft.chosen = chosen;
      renderProviders();
    } catch (e) { status(e.message, "err"); refresh.classList.remove("busy"); }
  };
  foot.append(add, refresh);
  if (p.fetched) foot.append(el("span", "hint", t("vendor list · {when}", { when: p.fetched })));
  else if (p.models.length) foot.append(el("span", "hint", t("from models.dev · Refresh asks the vendor")));
  box.append(foot);
  draw();
  return box;
}

async function providerAction(action, body, okMsg) {
  try {
    providers = await api("provider/" + action, body);
    editing = null;
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
  renderUsageLoading();
  usage = await api("usage?period=" + period);
  renderUsage();
}

function renderUsageLoading() {
  const view = $("#view-usage");
  view.classList.add("loading");
  view.setAttribute("aria-busy", "true");
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.disabled = true;
    seg.append(b);
  }
  slide(seg, "period");
  $("#usageCost").replaceChildren(el("span", "skeleton sk-cost"));
  const subscriptions = $("#subscriptionUsage");
  subscriptions.hidden = false;
  subscriptions.replaceChildren();
  for (let i = 0; i < 2; i++) {
    const card = el("div", "subscription-card skeleton-card");
    card.append(el("span", "skeleton sk-title"), el("span", "skeleton sk-line"), el("span", "skeleton sk-line short"));
    subscriptions.append(card);
  }
  const stats = $("#stats");
  stats.classList.remove("empty");
  stats.replaceChildren();
  for (let i = 0; i < 4; i++) {
    const tile = el("div", "kpi loading-kpi");
    tile.append(el("span", "skeleton sk-number"), el("span", "skeleton sk-label"));
    stats.append(tile);
  }
  $("#chart").hidden = true;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = true;
  for (const h of $$("#view-usage .row-head")) h.hidden = true;
  $("#usageNote").textContent = "";
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
  const view = $("#view-usage");
  view.classList.remove("loading");
  view.removeAttribute("aria-busy");
  const seg = $("#period");
  seg.replaceChildren();
  for (const [id, name] of PERIODS) {
    const b = el("button", "opt" + (id === period ? " on" : ""), t(name));
    b.onclick = () => { for (const x of seg.querySelectorAll(".opt")) x.classList.toggle("on", x === b); slide(seg, "period"); period = id; loadUsage().catch((e) => status(e.message, "err")); };
    seg.append(b);
  }
  slide(seg, "period");
  const cost = $("#usageCost");
  cost.replaceChildren();
  const c = fmtCost(u);
  if (c) {
    cost.append(el("b", "", "≈" + c), el("span", "", t("list price")));
    cost.title = u.unpriced ? t(u.unpriced === 1 ? "{n} call had no known price and is not counted" : "{n} calls had no known price and are not counted", { n: u.unpriced }) : t("At each model's list price on models.dev");
  } else if (u.calls) {
    cost.append(el("span", "", t("no price for these models")));
  }

  const subscriptions = $("#subscriptionUsage");
  subscriptions.replaceChildren();
  subscriptions.hidden = !u.subscriptions?.length;
  for (const sub of u.subscriptions || []) {
    const card = el("div", "subscription-card");
    const head = el("div", "subscription-head");
    head.append(icon(sub.icon), el("b", "", sub.name));
    if (sub.plan) head.append(el("span", "plan", sub.plan));
    card.append(head);
    if (sub.error) {
      card.append(el("div", "subscription-error", t("Usage unavailable")));
      card.title = sub.error;
    } else {
      const windows = el("div", "quota-windows");
      for (const w of sub.windows) {
        const quota = el("div", "quota");
        const labels = el("div", "quota-labels");
        labels.append(el("span", "", t(w.name)), el("b", "", w.display || `${Math.round(w.used)}%`));
        const track = el("div", "quota-track");
        const fill = el("i");
        fill.style.width = `${Math.max(0, Math.min(100, w.used))}%`;
        track.append(fill);
        quota.append(labels, track);
        if (w.resetsAt) quota.title = t("Resets {when}", { when: new Date(w.resetsAt).toLocaleString() });
        windows.append(quota);
      }
      card.append(windows);
    }
    subscriptions.append(card);
  }

  const stats = $("#stats");
  stats.replaceChildren();
  const empty = !u.calls;
  $("#chart").hidden = empty;
  for (const id of ["usageAgents", "usageModels"]) $("#" + id).hidden = empty;
  for (const h of $$("#view-usage .row-head")) h.hidden = empty;
  if (empty) {
    stats.classList.add("empty");
    const none = { today: "No calls today.", "7d": "No calls in the last 7 days.", "30d": "No calls in the last 30 days.", all: "No calls yet." }[period];
    stats.append(el("div", "none", t(none) + " " + t("Point an agent at a catalog model and use it; every call through the gateway is counted here.")));
    $("#usageNote").textContent = "";
    return;
  }
  stats.classList.remove("empty");
  const tile = (n, label, sub, title) => {
    const t = el("div", "kpi");
    if (title) t.title = title;
    t.append(el("b", "", n), el("span", "", label));
    if (sub) t.append(el("small", "", sub));
    stats.append(t);
  };
  tile(fmtN(tokensOf(u)), t("tokens"), t("{a} in · {b} out", { a: fmtN(u.input), b: fmtN(u.output) }));
  // cache reads are billed at a fraction of input, so how much of the prompt
  // came from cache is the number that explains the bill; input here already
  // excludes the cached tokens (the gateway subtracts them). The written
  // count is secondary and only fits in the tooltip.
  const promptTokens = u.input + u.cache_read;
  const hit = u.cache_read && promptTokens ? t("hit rate {p}", { p: Math.round(100 * u.cache_read / promptTokens) + "%" }) : "";
  tile(fmtN(u.cache_read), t("cache read"), hit, u.cache_write ? t("{n} written", { n: fmtN(u.cache_write) }) : "");
  tile(fmtN(u.reasoning), t("reasoning"), t("inside output"));
  tile(String(u.calls), t(u.calls === 1 ? "call" : "calls"), u.errors ? t("{n} failed", { n: u.errors }) : "");

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
    const when = u.bucket === "hour" ? `${p.label}:00` : u.bucket === "week" ? t("week of {label}", { label: p.label }) : p.label;
    b.title = p.calls ? t(p.calls === 1 ? "{when} · {tokens} tokens · {n} call" : "{when} · {tokens} tokens · {n} calls", { when, tokens: fmtN(tokensOf(p)), n: p.calls }) + (fmtCost(p) ? " · ≈" + fmtCost(p) : "") : t("{when} · nothing", { when });
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
      sub.push(t(g.calls === 1 ? "{n} call" : "{n} calls", { n: g.calls }));
      if (g.errors) sub.push(t("{n} failed", { n: g.errors }));
      who.append(el("div", "sub", sub.join(" · ")));
      r.append(who);
      const share = el("div", "share");
      const fill = el("i");
      fill.style.width = Math.max(1.5, 100 * tokensOf(g) / total).toFixed(1) + "%";
      share.append(fill);
      share.title = t("{n}% of tokens", { n: Math.round(100 * tokensOf(g) / total) });
      r.append(share);
      const num = el("div", "num");
      num.append(el("b", "", fmtN(tokensOf(g))), el("small", "", t("{a} in · {b} out", { a: fmtN(g.input), b: fmtN(g.output) }) + (g.cache_read ? " · " + t("{n} cached", { n: fmtN(g.cache_read) }) : "")));
      r.append(num);
      r.append(el("div", "cost", fmtCost(g) ? "≈" + fmtCost(g) : ""));
      box.append(r);
    }
  };
  list("usageAgents", u.agents);
  list("usageModels", u.models);
  $("#usageNote").textContent = t("Counted from the providers' own usage reports on every call through the gateway · {path}", { path: u.path });
}

// ---------- settings ----------
//
// Two choices (palette, language) and the facts people come looking for:
// the version, where magpie keeps its files, the gateway's address.

const THEMES = [["system", "System"], ["light", "Light"], ["dark", "Dark"]];
const LOCALES = [["system", "System"], ["en", "English"], ["zh", "中文"]];

// applyPrefs paints and speaks as the saved settings say. A ?theme= or
// ?locale= in the URL wins, so a forced look stays forced.
function applyPrefs(s) {
  s = s || {};
  const root = document.documentElement;
  if (!params.get("theme")) {
    const want = !s.theme || s.theme === "system" ? undefined : s.theme;
    if (root.dataset.theme !== want) {
      if (applyPrefs.ready) { // not on the first paint
        root.classList.add("theming");
        clearTimeout(applyPrefs.t);
        applyPrefs.t = setTimeout(() => root.classList.remove("theming"), 450);
      }
      if (want) root.dataset.theme = want; else delete root.dataset.theme;
    }
  }
  applyPrefs.ready = true;
  const was = locale;
  setLocale(s.lang);
  if (was !== locale && mode === "window") queueMicrotask(() => slide($("#nav"), "nav"));
  return was !== locale;
}

async function loadSettings() {
  prefs = await api("settings");
  renderSettings();
}

function renderSettings() {
  const s = prefs;
  $("#themeSegs").replaceChildren(segs(THEMES.map(([id, name]) => [id, t(name)]), s.theme, (theme) => savePrefs({ theme, lang: s.lang })));
  $("#langSegs").replaceChildren(segs(LOCALES.map(([id, name]) => [id, t(name)]), s.lang, (lang) => savePrefs({ theme: s.theme, lang })));

  const about = $("#about");
  about.replaceChildren();
  const row = (name, sub, value, ...tools) => {
    const r = el("div", "row pref");
    const who = el("div", "who");
    who.append(el("div", "name", name));
    if (sub) who.append(el("div", "sub", sub));
    const val = el("div", "val");
    val.append(el("code", "", value), ...tools);
    r.append(who, val);
    about.append(r);
    return r;
  };
  renderUpdate(row(t("Version"), "", s.version));
  const open = el("button", "text", t("Open"));
  open.onclick = () => api("open", { url: "file://" + s.path });
  row(t("Config folder"), t("providers, profiles and these settings"), s.dir, copyBtn(s.dir, t("Path")), open);
  row(t("Gateway URL"), t("the address every agent is pointed at"), s.gateway, copyBtn(s.gateway, t("Gateway URL")));
}

// renderUpdate fills in the version row: whether a newer magpie is out.
// The app checks and downloads on its own, so usually the row just offers
// the restart; a check can also be asked for.
async function renderUpdate(r, u) {
  u = u || await api("update").catch(() => null);
  if (!u || !r.isConnected) return;
  const who = r.querySelector(".who"), val = r.querySelector(".val");
  const sub = who.querySelector(".sub") || who.appendChild(el("div", "sub"));
  sub.title = "";
  for (const b of val.querySelectorAll("button")) b.remove();
  const btn = (label, fn) => { const b = el("button", "text", label); b.onclick = fn; val.append(b); };
  const check = async () => {
    sub.textContent = t("Checking for updates…");
    renderUpdate(r, await api("update/check", {}).catch((e) => ({ state: "error", error: e.message })));
  };
  switch (u.state) {
    case "ready":
      sub.textContent = t("{v} is downloaded", { v: u.latest });
      btn(t("Restart to update"), () => api("update/install", {}));
      break;
    case "available":
      sub.textContent = t("{v} is out", { v: u.latest });
      btn(t("Download"), () => api("update/install", {}));
      break;
    case "downloading":
      sub.textContent = t("Downloading {v}…", { v: u.latest });
      setTimeout(() => renderUpdate(r), 2000);
      break;
    case "checking":
      sub.textContent = t("Checking for updates…");
      setTimeout(() => renderUpdate(r), 1000);
      break;
    case "latest":
      sub.textContent = t("Up to date");
      btn(t("Check"), check);
      break;
    case "error":
      sub.textContent = t("Couldn't check for updates");
      sub.title = u.error || "";
      btn(t("Check"), check);
      break;
    default: // built from source, or not asked yet
      sub.textContent = "";
  }
}

async function savePrefs(body) {
  try {
    prefs = await api("settings", body);
    state.settings = prefs;
    const spoke = applyPrefs(prefs);
    renderSettings();
    if (spoke) { renderAgents(); providers = null; usage = null; }
    status(t("Saved"), "ok", 1500);
  } catch (e) {
    status(e.message, "err");
  }
}

// ---------- header / footer ----------

function show(v) {
  view = v;
  if (mode === "window") { for (const b of $("#nav").querySelectorAll("button")) b.classList.toggle("on", b.dataset.view === v); slide($("#nav"), "nav"); }
  $("#prefs").classList.toggle("on", v === "settings");
  for (const id of ["agents", "providers", "gateway", "usage", "settings"]) $("#view-" + id).hidden = v !== id;
  closePicker();
  if (v !== "providers" && editing !== null) cancelEdit();
  if (v === "providers" || v === "gateway") loadProviders().catch((e) => status(e.message, "err"));
  if (v === "usage") loadUsage().catch((e) => status(e.message, "err"));
  if (v === "settings") loadSettings().catch((e) => status(e.message, "err"));
}
if (mode === "window") for (const b of $("#nav").querySelectorAll("button")) b.onclick = () => { show(b.dataset.view); b.blur(); };
$("#prefs").onclick = () => { if (mode === "window") show("settings"); else api("window/main?view=settings", {}); $("#prefs").blur(); };

$("#sync").onclick = async () => {
  const b = $("#sync");
  b.classList.add("spin");
  try {
    state = await api("sync", {});
    renderAgents();
    if (providers) await loadProviders();
    status(t("Model lists refreshed"), "ok");
  } catch (e) {
    status(t("Sync failed: {e}", { e: e.message }), "err");
  } finally {
    b.classList.remove("spin");
  }
};
$("#open").onclick = () => api("window/main", {});
$("#openMain").onclick = () => api("window/main", {});
$("#quit").onclick = () => api("window/quit", {});
if (mode === "window") { $("#open").remove(); $("#openMain").remove(); $("#quit").remove(); }
else { $("#nav").remove(); }

// Config files may change underneath us (another magpie, an editor); reload when
// the panel comes back into view.
document.addEventListener("visibilitychange", () => { if (!document.hidden) load(); });
window.addEventListener("focus", load);
if (mode === "window" && ["providers", "gateway", "usage", "settings"].includes(params.get("view"))) show(params.get("view"));
else if (mode === "window") slide($("#nav"), "nav");
load();
