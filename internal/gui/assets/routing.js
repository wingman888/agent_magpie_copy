// Routing, live: the gateway's own trace of each request, played as it
// happens. A request travels from the agent to magpie and on to the account
// routing put first; one that can't answer sends it back, and the next
// takes it. Every row, number and sentence comes from what the gateway
// recorded while deciding (see internal/gateway/trace.go) — the order it
// weighed the accounts in, what it weighed them by, what each answered,
// how long a failed one rests. Nothing here is worked out again or made up.
(() => {
  const box = $("#rt");
  if (!box) return;
  const NS = "http://www.w3.org/2000/svg";
  const still = () => matchMedia("(prefers-reduced-motion: reduce)").matches;
  const shown = () => !$("#view-routing").hidden && !document.hidden;

  // ---------- the stage ----------

  const top = el("div", "rt-top");
  const what = el("div", "rt-what");
  const mode = el("p", "rt-mode");
  top.append(what, mode);
  const stage = el("div", "rt-stage");
  const wires = document.createElementNS(NS, "svg");
  wires.setAttribute("class", "rt-wires");
  wires.setAttribute("aria-hidden", "true");
  const src = el("div", "rt-node rt-src");
  const srcIc = el("span", "rt-ic"), srcName = el("b"), srcSub = el("small");
  src.append(srcIc, srcName, srcSub);
  const hub = el("div", "rt-node rt-hub");
  const bird = document.createElementNS(NS, "svg");
  bird.setAttribute("viewBox", "0 0 44 44");
  bird.setAttribute("class", "rt-bird");
  bird.innerHTML = '<use href="#bird"/>';
  const hubSub = el("small"), chip = el("i");
  hub.append(bird, el("b", "", "magpie"), hubSub, chip);
  const list = el("ol", "rt-accts");
  stage.append(wires, src, hub, list);
  const foot = el("div", "rt-foot");
  const cap = el("p", "rt-cap");
  cap.setAttribute("aria-live", "polite");
  const stats = el("div", "rt-stats");
  const statB = [];
  for (const k of ["requests", "rerouted", "errors your agent saw"]) {
    const s = el("span"), b = el("b", "", "0");
    s.append(b, el("span", "", k));
    s.dataset.label = k;
    statB.push(b);
    stats.append(s);
  }
  foot.append(cap, stats);
  const log = el("div", "rt-log");
  const logHead = el("div", "rt-log-head");
  const steps = el("ol", "rt-steps");
  log.append(logHead, steps);
  const off = el("div", "none rt-off");
  box.append(top, stage, foot, log, off);

  // under the stage: every request the gateway keeps, and each account or
  // key as those requests found it
  const more = $("#rtMore");
  const reqHead = el("div", "row-head"), reqNote = el("span", "note");
  const reqs = el("div", "list rt-reqs");
  const actHead = el("div", "row-head"), actNote = el("span", "note");
  const acts = el("div", "list rt-acts");
  const hist = el("div", "rt-cols");
  const colA = el("div", "rt-col"), colB = el("div", "rt-col");
  colA.append(reqHead, reqs);
  colB.append(actHead, acts);
  hist.append(colA, colB);
  more.append(hist);

  const path = () => { const p = document.createElementNS(NS, "path"); wires.appendChild(p); return p; };
  const wSrc = path();
  const tick = (e) => { e.classList.remove("tick"); void e.offsetWidth; e.classList.add("tick"); };

  // ---------- words ----------

  let skew = 0; // the gateway's clock less this page's
  const now = () => Date.now() + skew;
  const at = (s) => new Date(s).getTime();
  const known0 = (s) => s && !s.startsWith("0001-");
  function dur(ms) {
    const s = Math.max(1, Math.round(ms / 1000));
    if (s < 60) return t("{n} s", { n: s });
    const m = Math.round(s / 60);
    if (m < 60) return t("{n} min", { n: m });
    const h = Math.floor(m / 60), mm = m % 60;
    if (h < 10 && mm) return t("{h} h {m} min", { h, m: mm });
    if (h < 48) return t("{n} h", { n: Math.round(m / 60) });
    return t("{n} d", { n: Math.round(m / 1440) });
  }
  const took = (ms) => ms < 1000 ? t("{n} ms", { n: ms }) : t("{n} s", { n: (ms / 1000).toFixed(ms < 10e3 ? 1 : 0) });
  function clock(s) {
    const d = new Date(s), n = new Date();
    const hm = d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit" });
    return d.toDateString() === n.toDateString() ? hm : d.toLocaleDateString([], { weekday: "short" }) + " " + hm;
  }
  const tokens = (n) => n >= 1e6 ? (n / 1e6).toFixed(1) + "M" : n >= 1e3 ? (n / 1e3).toFixed(1) + "k" : String(Math.round(n));
  const pct = (n) => Math.round(n) + "%";
  const FAIL = { rate: "rate limited", credit: "out of credit", quota: "quota used up", other: "failed" };
  const failWord = (why) => t(FAIL[why] || "failed");
  const API = { anthropic: "Anthropic", chat: "OpenAI", responses: "OpenAI Responses", gemini: "Gemini" };
  const MODES = {
    "": ["Smart", "Smart: of the accounts with quota to spare, the one whose allowance renews soonest goes first — what it has left would be lost at the reset. One at 90% or more waits until the others can't answer; one resting after a failure goes last."],
    order: ["In order", "In order: the first answers everything until it can't; then the next."],
    rotate: ["In turn", "In turn: each request starts one account further along."],
    usage: ["Least used", "Least used first: the account with the most of its allowance left goes first; a key by the tokens magpie sent it lately."],
  };
  const KEYS_SMART = "Smart: keys that suit the request go first — one made for the model's own API — then in their order. One resting after a failure goes last.";

  const agentOf = (id) => state.agents.find((a) => a.id === id);
  const agentName = (id) => agentOf(id)?.name || (id && id !== "other" ? id : t("your agent"));
  // who names an account or key in a sentence
  const who = (w) => w.kind === "provider" ? w.name : w.who;
  const group = (w) => w.used >= 98 ? "spent" : w.used >= 90 ? "low" : "fine";
  const renews = (w) => (w.renews || []).map((s) => known0(s) ? at(s) : 0);

  // cmpRenews orders two accounts' windows as the gateway does: the
  // biggest first, to the hour, one not known after those known. It says
  // which window decided, too.
  function cmpRenews(a, b) {
    const ra = renews(a), rb = renews(b), H = 3600e3;
    for (let k = 0; k < ra.length || k < rb.length; k++) {
      const x = ra[k] ? Math.floor(ra[k] / H) : 0, y = rb[k] ? Math.floor(rb[k] / H) : 0;
      if (x === y) continue;
      if (!x || !y) return { c: x ? -1 : 1, k };
      return { c: x < y ? -1 : 1, k };
    }
    return { c: 0, k: -1 };
  }

  // how long a rest is from a moment: now for a row, the moment it was
  // decided for the story of a request
  function restWhen(rest, from = now()) {
    const left = at(rest.until) - from;
    return left < 3600e3 ? t("back in {d}", { d: dur(left) }) : t("back at {time}", { time: clock(rest.until) });
  }
  // restHow says how long a failed account sits out, and what said so.
  function restHow(rest, from) {
    const d = dur(at(rest.until) - from), time = clock(rest.until);
    switch (rest.by) {
      case "retry-after": return t("It rests {d}, as the vendor's Retry-After says", { d });
      case "credit": return t("It sits out half an hour, until someone tops it up");
      case "window": return t("Its allowance is used up: it rests until that renews, at {time}", { time });
      case "resets": return t("It rests until {time}, when Claude Code says the limit resets", { time });
      case "quota": return t("It rests 15 minutes: out of quota, with no word of when it resets");
      case "backoff": return rest.failures > 1
        ? t("It has failed {n} times in a row: it rests {d}, longer each time", { n: rest.failures, d })
        : t("It rests {d}, longer if it fails again", { d });
      case "cooldown": return rest.why === "rate"
        ? t("It cools down {d}: the vendor didn't say for how long", { d })
        : t("It rests {d}", { d });
      default: return t("It rests {d}", { d });
    }
  }

  // why routing put the first where it did
  function firstWhy(r) {
    const f = r.order[0];
    if (!f) return t("Nothing could take {model}.", { model: r.model });
    const w = who(f);
    if (r.order.length === 1) {
      if (f.rest) return t("{who} is the only one, so it's tried though it is resting.", { who: w });
      return f.kind === "account" ? t("{who} is the only account on for {model} — nothing to choose between.", { who: w, model: r.model })
        : t("{name} has one key on — nothing to choose between.", { name: f.name });
    }
    if (f.rest) return t("Every one is resting after a failure, so {who}, first in line, is tried all the same.", { who: w });
    if (f.fallback) {
      const name = r.order.find((x) => !x.fallback)?.name || r.provider;
      return t("{name} is resting, so its fallback {fb} goes first.", { name, fb: `${f.provider}/${f.model}` });
    }
    const peers = r.order.filter((x) => x !== f && !x.fallback && !x.rest);
    const rested = r.order.filter((x) => x !== f && !x.fallback && x.rest).map(who);
    const restedTo = (s) => rested.length ? t("With {rested} resting after a failure, {who} goes first: ", { rested: rested.join(", "), who: w }) + s : null;
    switch (f.routing) {
      case "order": return rested.length
        ? t("In order: with {rested} resting after a failure, {who} is the first that can answer.", { rested: rested.join(", "), who: w })
        : t("In order: {who} is first, and answers everything while it can.", { who: w });
      case "rotate": return t("In turn: it's {who}'s turn — each request starts one further along.", { who: w });
      case "usage":
        if (f.kind === "account" && f.known) return t("Least used first: {who} has the most of its allowance left — {n} used.", { who: w, n: pct(f.used) });
        if (f.kind === "key") return t("Least used first: {who} served the fewest tokens lately — {n}.", { who: w, n: tokens(f.tokens || 0) });
        return t("Least used first: {who} goes first.", { who: w });
    }
    if (f.kind === "key") {
      if (!f.fit && peers.some((p) => p.fit > 0)) return t("{who} goes first: it's made for {api}, the API {model} is at home in, so nothing is translated.", { who: w, api: API[f.speaks] || f.speaks, model: f.model });
      return restedTo(t("the others go in their order.")) || t("{who} goes first: keys go in their order, those that suit the request first.", { who: w });
    }
    if (f.kind !== "account") return t("{who} goes first.", { who: w });
    if (!f.known && !peers.some((p) => p.known)) return t("The vendor hasn't said yet what these accounts have left, so they go in their order: {who} first.", { who: w });
    if (group(f) !== "fine") return t("Every account is at 90% or more of its allowance, so the one with the most left goes first: {who}, at {n}.", { who: w, n: pct(f.used) });
    const next = peers.find((p) => p.known && group(p) === "fine");
    const soon = renews(f).find(Boolean);
    if (!next) return t("{who} goes first: it has quota to spare, and the others are kept for last.", { who: w });
    const { c, k } = cmpRenews(f, next);
    if (c < 0 && k === 0) return t("{who} goes first: of those with quota to spare, its allowance renews soonest — in {d} — and what it has left then is lost. {other} renews later and keeps its own.", { who: w, d: dur(renews(f)[0] - at(r.time)), other: who(next) });
    if (c < 0) return t("{who} goes first: its allowance renews in the same hour as {other}'s, and its shorter one sooner — in {d}.", { who: w, other: who(next), d: dur(renews(f)[k] - at(r.time)) });
    if (soon) return t("{who} and {other} renew within the same hour, so the order given stays — and the vendor's prompt cache stays warm.", { who: w, other: who(next) });
    return t("{who} goes first, in the order given: when its allowance renews isn't known.", { who: w });
  }

  // asides: the others' places, where they say something
  function asides(r) {
    const out = [];
    const smart = (x) => !x.routing && x.kind === "account";
    const someKnown = r.order.some((x) => x.known);
    for (const x of r.order.slice(1)) {
      if (x.rest) out.push(t("{who} is resting — {why}, {when} — so it waits at the back.", { who: who(x), why: `${x.rest.status} · ${failWord(x.rest.why)}`, when: restWhen(x.rest, at(r.time)) }));
      else if (smart(x) && x.known && group(x) === "spent") out.push(t("{who} is at {n} — all but used up, it answers only when nothing else can.", { who: who(x), n: pct(x.used) }));
      else if (smart(x) && x.known && group(x) === "low") out.push(t("{who} is at {n} — kept for when the others can't.", { who: who(x), n: pct(x.used) }));
      else if (smart(x) && !x.known && someKnown) out.push(t("{who}: what it has left isn't known yet, so it goes after those known.", { who: who(x) }));
    }
    for (const x of r.left || []) out.push(t("{who} is left out: its plan doesn't list {model}.", { who: who(x), model: x.model }));
    return out;
  }

  function tryWhy(r, i) {
    const tr = r.tries[i], w = r.order.find((x) => x.id === tr.id), agent = agentName(r.agent);
    const name = w ? `${who(w)} (${w.model})` : tr.id;
    if (!tr.done) return t("{who} is answering…", { who: name });
    if (tr.status < 400) {
      const tk = r.tokens ? " · " + t("{n} tokens", { n: tokens(r.tokens) }) : "";
      return i > 0
        ? t("{who} answered in {ms}{tk}. {agent} got one clean reply and never saw the {n} that failed first.", { who: name, ms: took(tr.ms), tk, agent, n: i })
        : t("{who} answered in {ms}{tk}.", { who: name, ms: took(tr.ms), tk });
    }
    if (tr.rest) {
      const next = r.tries[i + 1], nw = next && r.order.find((x) => x.id === next.id);
      return t("{who} answered {status} · {fail}. {how}; the request goes on to {next} before any of the reply reaches {agent}.",
        { who: name, status: tr.status, fail: failWord(tr.fail), how: restHow(tr.rest, at(tr.start) + (tr.ms || 0)), next: nw ? who(nw) : t("the next"), agent });
    }
    const last = i === r.order.length - 1;
    return last
      ? t("{who} answered {status} and nobody is left to try, so {agent} gets the error.", { who: name, status: tr.status, agent })
      : t("{who} answered {status} — an error another account wouldn't fix, so {agent} gets it.", { who: name, status: tr.status, agent });
  }

  // ---------- state ----------

  const routes = new Map(); // id → the latest of each route
  let seq = 0, mine = true, loaded = false;
  let cur = null;           // the route the stage shows
  let pinned = null;        // a past route picked from the strip
  let rows = new Map();     // id → { li, wire, st, bi, tg, w }
  let key = "";             // the ids the stage has rows for
  let gen = 0, trips = [], waiters = [], active = 0, queue = [];
  let capQ = [], capAt = -1e9, capLo = false, flipUntil = 0;

  const travel = (dot, p, rev, ms) => new Promise((res) => trips.push({ dot, p, rev, t0: performance.now(), ms: still() || !shown() ? 0 : ms, res, g: gen }));
  const until = (f) => f() ? Promise.resolve() : new Promise((res) => waiters.push({ f, res }));
  const wake = () => { const w = waiters; waiters = []; for (const x of w) if (x.f()) x.res(); else waiters.push(x); };
  function say(s, lo) {
    if (!s) return;
    if (!shown()) { capQ = []; show({ s, lo }); return; }
    if (lo && capQ.length) return;
    if (!lo) capQ = capQ.filter((c) => !c.lo);
    capQ.push({ s, lo });
    if (capQ.length > 3) capQ.shift();
  }
  function show(c) { capLo = !!c.lo; cap.textContent = c.s; cap.classList.remove("in"); void cap.offsetWidth; cap.classList.add("in"); capAt = performance.now(); }

  function layout() {
    const r = stage.getBoundingClientRect();
    if (!r.width) return;
    const b = (e) => { const x = e.getBoundingClientRect(); return { l: x.left - r.left, r: x.right - r.left, t: x.top - r.top, b: x.bottom - r.top, cx: (x.left + x.right) / 2 - r.left, cy: (x.top + x.bottom) / 2 - r.top }; };
    wires.setAttribute("viewBox", `0 0 ${r.width} ${r.height}`);
    const s = b(src), h = b(hub);
    wSrc.setAttribute("d", h.l > s.r ? `M${s.r} ${s.cy} L${h.l} ${h.cy}` : `M${s.cx} ${s.b} L${h.cx} ${h.t}`);
    for (const row of rows.values()) {
      const a = b(row.li);
      if (a.l > h.r) {
        const mx = (h.r + a.l) / 2;
        row.wire.setAttribute("d", `M${h.r} ${h.cy} C${mx} ${h.cy} ${mx} ${a.cy} ${a.l} ${a.cy}`);
      } else { // the accounts sit under magpie: a lane down their left
        const x = a.l - 12;
        row.wire.setAttribute("d", `M${h.cx} ${h.b} C${h.cx} ${h.b + 22} ${x} ${h.b + 2} ${x} ${h.b + 24} L${x} ${a.cy - 10} Q${x} ${a.cy} ${a.l} ${a.cy}`);
      }
    }
  }

  // stageFor puts a route's accounts on the stage: in the order it weighed
  // them, those left out after. The same accounts in another order move
  // to their places, so a new order shows.
  function stageFor(r) {
    const all = [...r.order, ...(r.left || [])];
    const k = all.map((w) => w.id).sort().join("\n");
    if (k !== key) {
      key = k;
      for (const row of rows.values()) row.wire.remove();
      rows = new Map();
      list.replaceChildren();
      for (const w of all) {
        const li = el("li"), b = el("b");
        b.append(icon(w.icon || (w.preset ? w.preset : "generic")));
        const name = el("span", "who", who(w));
        // the provider's name heads the card; a row names what differs
        const sub = el("span", "", w.fallback ? w.name : w.kind === "provider" ? "" : w.plan || "");
        b.append(name, " ", sub, el("code", "mdl", w.model));
        if (w.fallback) b.append(el("small", "fb", t("fallback")));
        const st = el("em"), bar = el("div", "bar"), bi = el("i"), tg = el("span", "tag");
        bar.append(bi);
        li.append(el("i", "dot"), b, st, bar, tg);
        li.title = w.id;
        list.append(li);
        rows.set(w.id, { li, wire: path(), st, bi, tg, w });
      }
    } else {
      // FLIP: from where each was to its new place
      const before = new Map([...rows].map(([id, row]) => [id, row.li.getBoundingClientRect().top]));
      for (const w of all) list.append(rows.get(w.id).li);
      let moved = false;
      for (const [id, row] of rows) {
        const dy = before.get(id) - row.li.getBoundingClientRect().top;
        if (!dy || still()) continue;
        moved = true;
        row.li.style.transition = "none";
        row.li.style.transform = `translateY(${dy}px)`;
      }
      if (moved) {
        void list.offsetWidth;
        for (const row of rows.values()) { row.li.style.transition = ""; row.li.style.transform = ""; }
        flipUntil = performance.now() + 520;
      }
    }
    for (const w of all) rows.get(w.id).w = w;
    cur = r;
    const ag = agentOf(r.agent);
    srcIc.replaceChildren(icon(ag?.icon || "generic"));
    srcName.textContent = agentName(r.agent);
    srcSub.textContent = r.model;
    const f = r.order.find((x) => !x.fallback) || r.order[0];
    const m = MODES[f?.routing || ""] || MODES[""];
    chip.textContent = t(m[0]);
    chip.hidden = false;
    hubText();
    what.replaceChildren(el("b", "", f?.name || r.provider), el("span", "", " · " + t(r.order.filter((x) => !x.fallback).length + (r.left || []).length === 1 ? "one on" : "{n} on", { n: r.order.filter((x) => !x.fallback).length + (r.left || []).length })));
    mode.textContent = t(!f?.routing && f?.kind === "key" ? KEYS_SMART : m[1]);
    layout();
  }

  // what a row says now: resting, answering, or what routing weighed it by
  function render() {
    if (!cur) return;
    hubText();
    const r = pinned || cur, n = now();
    const trying = new Set(), answered = new Set(), rests = new Map(), gave = new Map();
    for (const w of r.order) if (w.rest) rests.set(w.id, w.rest);
    for (const tr of r.tries) {
      if (!tr.done) trying.add(tr.id);
      else if (tr.status < 400) answered.add(tr.id);
      else if (!tr.rest) gave.set(tr.id, tr); // the error the agent got
      if (tr.rest) rests.set(tr.id, tr.rest);
    }
    const onWire = new Set(flying.values());
    for (const [id, row] of rows) {
      const w = row.w, rest = rests.get(id), resting = rest && at(rest.until) > n;
      let s;
      if (w.unlisted) s = t("its plan doesn't list {model}", { model: w.model });
      else if (resting) s = `${failWord(rest.why)} · ${restWhen(rest)}`;
      else if (trying.has(id)) s = t("answering…");
      else if (answered.has(id)) s = t("answered this request");
      else if (gave.has(id)) s = t("{status} · {fail} · passed to {agent}", { status: gave.get(id).status, fail: failWord(gave.get(id).fail), agent: agentName(r.agent) });
      else if (w.kind === "account" && w.known) {
        const soon = renews(w)[0];
        s = !w.routing && w.used >= 98 ? t("{n} used · all but used up", { n: pct(w.used) })
          : !w.routing && w.used >= 90 ? t("{n} used · kept for last", { n: pct(w.used) })
          : soon ? t("{n} used · renews in {d}", { n: pct(w.used), d: dur(soon - n) }) : t("{n} used", { n: pct(w.used) });
      } else if (w.kind === "account") s = t("what's left not known yet");
      else if (w.routing === "usage") s = t("{n} tokens lately", { n: tokens(w.tokens || 0) });
      else if (w.speaks) s = t("{api} only", { api: API[w.speaks] || w.speaks });
      else s = w.kind === "key" ? t("API key") : t("one key");
      if (row.st.textContent !== s) row.st.textContent = s;
      const bar = w.kind === "account" && w.known;
      row.li.classList.toggle("nobar", !bar);
      row.bi.style.width = bar ? Math.min(100, w.used) + "%" : "0";
      const on = !resting && (trying.has(id) || answered.has(id) || onWire.has(id));
      row.li.classList.toggle("on", on);
      row.li.classList.toggle("low", !!(!w.routing && w.known && w.used >= 90));
      row.li.classList.toggle("rest", !!resting || gave.has(id));
      row.li.classList.toggle("left", !!w.unlisted);
      row.wire.classList.toggle("live", on);
      row.wire.classList.toggle("rest", !!resting || !!w.unlisted);
    }
    wSrc.classList.toggle("live", trying.size > 0 || flying.size > 0);
  }

  // ---------- the log: how one request was routed ----------

  function renderLog() {
    const r = pinned || cur;
    log.hidden = !r;
    if (!r) return;
    logHead.replaceChildren(
      el("span", "", pinned ? t("How the request at {time} was routed", { time: clock(r.time) }) : t("How the last request was routed")),
      el("span", "grow"));
    if (pinned) {
      const live = el("button", "text", t("Back to live"));
      live.onclick = () => { pinned = null; stageFor(newest()); renderAll(); };
      logHead.append(live);
    }
    const items = [];
    const main = r.order.find((x) => !x.fallback);
    items.push([main && main.model !== r.model
      ? t("{agent} asked for {model}: {name} serves it, and the vendor is asked for {sent}", { agent: agentName(r.agent), model: r.model, name: main.name, sent: main.model })
      : t("{agent} asked for {model}", { agent: agentName(r.agent), model: r.model }) + " → " + (main?.name || r.provider), ""]);
    items.push([firstWhy(r), "why"]);
    for (const a of asides(r)) items.push([a, "aside"]);
    r.tries.forEach((_, i) => items.push([tryWhy(r, i), r.tries[i].done ? (r.tries[i].status < 400 ? "ok" : "bad") : "wait"]));
    if (r.done && !r.tries.length) items.push([t("Nothing was tried: {error}", { error: r.error || r.status }), "bad"]);
    steps.replaceChildren(...items.map(([s, c]) => el("li", c, s)));
  }

  // pick sets the stage to a past request, or back to live with the newest
  function pick(r) {
    pinned = r.id === newest()?.id ? null : r;
    gen++; trips = []; flying.clear(); for (const p of wires.querySelectorAll(".pkt")) p.remove();
    stageFor(pinned || r); renderAll();
    say(firstWhy(r));
    if (pinned) box.scrollIntoView({ block: "nearest", behavior: still() ? "auto" : "smooth" });
  }

  // who answered a request, or what its agent got
  function outcome(r) {
    if (!r.done) {
      const tr = r.tries[r.tries.length - 1], w = tr && r.order.find((x) => x.id === tr.id);
      return [w ? t("{who} is answering…", { who: `${who(w)} · ${w.model}` }) : t("routing…"), "wait"];
    }
    const ok = r.tries.find((tr) => tr.done && tr.status < 400), w = ok && r.order.find((x) => x.id === ok.id);
    if (r.status < 400) return [w ? `${who(w)} · ${w.model}` : r.provider, r.tries.length > 1 ? "moved" : "ok"];
    const last = r.tries[r.tries.length - 1];
    return [last ? `${r.status} · ${failWord(last.fail)}` : `${r.status || ""} ${r.error || ""}`.trim(), "bad"];
  }

  // the requests the gateway keeps, newest first: pick one to see how it was routed
  function renderHist() {
    const rs = [...routes.values()].sort((a, b) => b.id - a.id);
    hist.hidden = !rs.length;
    reqHead.replaceChildren(el("span", "label", t("Requests")), el("span", "grow"), reqNote);
    reqNote.textContent = t("the last {n} the gateway keeps", { n: rs.length });
    reqs.replaceChildren(...rs.map((r) => {
      const [said, how] = outcome(r);
      const b = el("button", "rt-req " + how);
      const sel = pinned ? pinned.id === r.id : cur?.id === r.id;
      b.setAttribute("aria-pressed", String(sel));
      const ag = agentOf(r.agent);
      const when = el("span", "at", new Date(r.time).toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" }));
      const asked = el("span", "asked");
      asked.append(icon(ag?.icon || "generic"), el("span", "m", r.model));
      const to = el("span", "to");
      to.append(el("i"), el("span", "", said));
      const meta = [];
      if (r.tries.length > 1) meta.push(t("{n} tries", { n: r.tries.length }));
      if (r.done && r.ms) meta.push(took(r.ms));
      if (r.tokens) meta.push(t("{n} tokens", { n: tokens(r.tokens) }));
      b.append(when, asked, to, el("span", "meta", meta.join(" · ")));
      b.title = `${agentName(r.agent)} · ${r.model} → ${r.provider}`;
      b.onclick = () => pick(r);
      return b;
    }));
    renderActs(rs);
  }

  // each account or key the kept requests weighed: how often it was
  // tried, answered and failed in them, and how the latest found it
  function renderActs(rs) {
    const by = new Map();
    for (const r of [...rs].reverse()) { // oldest first, so the latest wins
      const all = [...r.order, ...(r.left || [])];
      all.forEach((w, i) => {
        const a = by.get(w.id) || { w, tried: 0, ok: 0, fails: {}, last: 0, rest: null, restAt: 0, seen: 0, pos: 0, models: new Set() };
        a.w = w; a.seen++; a.pos = i; a.at = r.time;
        // a later request found it resting, or not
        if (at(r.time) >= a.restAt) { a.rest = w.rest || null; a.restAt = at(r.time); }
        by.set(w.id, a);
      });
      for (const tr of r.tries) {
        const a = by.get(tr.id);
        if (!a || !tr.done) continue;
        a.tried++;
        const end = at(tr.start) + (tr.ms || 0);
        if (tr.status < 400) { a.ok++; a.last = Math.max(a.last, end); const m = r.order.find((x) => x.id === tr.id)?.model; if (m) a.models.add(m); }
        else a.fails[tr.fail || "other"] = (a.fails[tr.fail || "other"] || 0) + 1;
        if (tr.rest && end >= a.restAt) { a.rest = tr.rest; a.restAt = end; }
      }
    }
    const list = [...by.values()].sort((x, y) => (x.w.name || "").localeCompare(y.w.name || "") || x.w.provider.localeCompare(y.w.provider) || x.pos - y.pos);
    actHead.replaceChildren(el("span", "label", t("Accounts and keys")), el("span", "grow"), actNote);
    actNote.textContent = t("over those requests");
    const n = now();
    let prov = "";
    const out = [];
    for (const a of list) {
      const w = a.w;
      if (w.provider !== prov) {
        prov = w.provider;
        const h = el("div", "rt-prov");
        h.append(icon(w.icon || w.preset || "generic"), el("b", "", w.name || w.provider));
        const m = MODES[list.find((x) => x.w.provider === prov && !x.w.fallback)?.w.routing || ""] || MODES[""];
        h.append(el("span", "", t(w.kind === "key" && !w.routing ? "Smart" : m[0])));
        out.push(h);
      }
      const row = el("div", "rt-act");
      const name = el("div", "nm");
      name.append(el("b", "", who(w)), el("span", "", w.kind === "provider" ? w.model : w.plan || (w.kind === "key" ? t("API key") : "")));
      let st, cls = "";
      const resting = a.rest && at(a.rest.until) > n;
      if (resting) { st = `${failWord(a.rest.why)} · ${restWhen(a.rest)}`; cls = "rest"; }
      else if (w.unlisted) { st = t("its plan doesn't list {model}", { model: w.model }); cls = "left"; }
      else if (w.kind === "account" && w.known) {
        const soon = renews(w)[0];
        st = soon && soon <= n ? t("{n} used at {time}; it has renewed since", { n: pct(w.used), time: clock(a.at) })
          : (soon ? t("{n} used · renews in {d}", { n: pct(w.used), d: dur(soon - n) }) : t("{n} used", { n: pct(w.used) })) + " · " + t("as of {time}", { time: clock(a.at) });
      } else if (w.kind === "account") st = t("what's left not known yet");
      else st = "";
      const tally = el("div", "tally");
      const fails = Object.entries(a.fails).map(([k, v]) => `${v} ${failWord(k)}`);
      tally.append(
        el("span", "", t("tried {n}", { n: a.tried })),
        el("span", "ok", t("answered {n}", { n: a.ok })),
        ...(fails.length ? [el("span", "bad", fails.join(", "))] : []),
        ...(a.last ? [el("span", "", t("last answered {time}", { time: clock(a.last) }))] : []),
        ...[...a.models].map((m) => el("code", "mdl", m)));
      row.append(name, el("div", "st " + cls, st), tally);
      if (w.kind === "account" && w.known) {
        const bar = el("div", "bar"), bi = el("i");
        bi.style.width = Math.min(100, w.used) + "%";
        bar.append(bi);
        row.append(bar);
      }
      row.title = w.id;
      out.push(row);
    }
    acts.replaceChildren(...out);
  }

  function renderAll() { render(); renderLog(); renderHist(); }
  const newest = () => [...routes.values()].reduce((a, b) => (!a || b.id > a.id ? b : a), null);

  // ---------- playing a request ----------

  const flying = new Map(); // packet → the id it is at

  async function play(id) {
    let r = routes.get(id);
    const k = [...r.order, ...(r.left || [])].map((w) => w.id).sort().join("\n");
    if (k !== key && active) {
      // another set of accounts: wait for the stage to be free
      queue.push(id);
      if (queue.length > 2) { queue.shift(); }
      return;
    }
    active++;
    const g = gen;
    stageFor(r);
    say(firstWhy(r));
    const aside = asides(r).find((s) => s);
    if (aside) say(aside, true);
    renderAll();
    const dot = document.createElementNS(NS, "circle");
    dot.setAttribute("r", 4.5);
    dot.setAttribute("class", "pkt");
    dot.setAttribute("cx", -20);
    wires.appendChild(dot);
    tick(src);
    await travel(dot, wSrc, false, 380);
    for (let i = 0; g === gen; ) {
      r = routes.get(id);
      if (i >= r.tries.length) {
        if (r.done) break;
        await until(() => g !== gen || routes.get(id).tries.length > i || routes.get(id).done);
        continue;
      }
      const row = rows.get(r.tries[i].id);
      if (!row) { i++; continue; }
      tick(hub);
      flying.set(dot, r.tries[i].id);
      await travel(dot, row.wire, false, 420);
      await until(() => g !== gen || routes.get(id).tries[i].done);
      if (g !== gen) break;
      r = routes.get(id);
      const tr = r.tries[i];
      if (cur.id === id) cur = r;
      if (tr.status < 400) {
        // the reply's tokens are counted once the route is done
        await until(() => g !== gen || routes.get(id).done);
        r = routes.get(id);
        say(tryWhy(r, i), true);
        renderAll();
        dot.classList.add("back");
        dot.setAttribute("r", 4);
        await travel(dot, row.wire, true, 360);
        flying.delete(dot);
        await travel(dot, wSrc, true, 320);
        break;
      }
      row.li.classList.remove("hit"); void row.li.offsetWidth; row.li.classList.add("hit", "tagged");
      row.tg.textContent = `${tr.status} · ${failWord(tr.fail)}`;
      setTimeout(() => row.li.classList.remove("tagged"), 1800);
      say(tryWhy(r, i));
      renderAll();
      await travel(dot, row.wire, true, 300);
      flying.delete(dot);
      if (!tr.rest) { // that was the answer: the agent gets the error
        dot.classList.add("err");
        await travel(dot, wSrc, true, 320);
        break;
      }
      i++;
    }
    flying.delete(dot);
    dot.remove();
    active--;
    renderAll();
    if (!active && queue.length) play(queue.shift());
  }

  // ---------- the loop ----------

  function frame(ts) {
    if (shown()) {
      const now_ = trips; trips = [];
      for (const tr of now_) {
        if (tr.g !== gen) { tr.res(); continue; }
        const k = tr.ms ? Math.min(1, (ts - tr.t0) / tr.ms) : 1;
        if (tr.dot && tr.p.getTotalLength) {
          const e = k < .5 ? 2 * k * k : 1 - (-2 * k + 2) ** 2 / 2;
          const len = tr.p.getTotalLength(), pt = tr.p.getPointAtLength((tr.rev ? 1 - e : e) * len);
          tr.dot.setAttribute("cx", pt.x);
          tr.dot.setAttribute("cy", pt.y);
        }
        if (k >= 1) tr.res(); else trips.push(tr);
      }
      if (ts < flipUntil) layout();
      if (capQ.length && ts - capAt > (capLo && !capQ[0].lo ? 500 : 1700)) show(capQ.shift());
    } else {
      for (const tr of trips) tr.res();
      trips = [];
      if (capQ.length) { show(capQ[capQ.length - 1]); capQ = []; }
    }
    requestAnimationFrame(frame);
  }
  // countdowns tick once a second
  setInterval(() => { if (shown()) { render(); renderActs([...routes.values()].sort((a, b) => b.id - a.id)); } }, 1000);

  function offline(msg) {
    off.textContent = msg;
    off.hidden = !msg;
    for (const e of [top, stage, foot, log]) e.hidden = !!msg;
    more.hidden = !!msg;
  }

  function empty() {
    offline("");
    what.replaceChildren(el("b", "", t("Waiting for a request")));
    mode.textContent = t("Send one from any agent routed through magpie and it plays here as it happens: who routing put first and why, each try, and what each answered.");
    srcIc.replaceChildren(icon("generic"));
    srcName.textContent = t("your agent");
    srcSub.textContent = "";
    chip.hidden = true;
    hubText();
    list.replaceChildren(el("li", "idle", t("No request yet")));
    say(t("Every request an agent sends to magpie shows up here, routed for real."));
    log.hidden = hist.hidden = true;
    layout();
  }

  async function poll() {
    for (;;) {
      try {
        const res = await fetch(`/api/gateway/trace?after=${seq}${loaded ? "&wait=1" : ""}`);
        const d = await res.json();
        skew = at(d.now) - Date.now();
        mine = d.mine;
        hubText();
        statB[0].textContent = d.totals.requests;
        statB[1].textContent = d.totals.rerouted;
        statB[2].textContent = d.totals.errors;
        if (!mine) {
          offline(t(providers?.gateway?.running ? "Another magpie serves the gateway; its routing plays live in that magpie's window." : "The gateway isn't running, so nothing is routed."));
          loaded = false;
          await new Promise((r) => setTimeout(r, 5000));
          continue;
        }
        if (d.seq < seq) routes.clear(); // the gateway started over
        seq = d.seq;
        const first = !loaded;
        const fresh = [];
        for (const r of d.routes) {
          if (!routes.has(r.id) && !first) fresh.push(r.id);
          routes.set(r.id, r);
        }
        for (const id of [...routes.keys()].sort((a, b) => a - b).slice(0, -60)) routes.delete(id);
        loaded = true;
        if (first) {
          offline("");
          const r = newest();
          if (r) { stageFor(r); say(firstWhy(r)); renderAll(); } else empty();
        } else {
          if (cur && routes.has(cur.id) && !active) cur = routes.get(cur.id);
          if (pinned && routes.has(pinned.id)) pinned = routes.get(pinned.id);
          for (const id of fresh) if (!pinned) play(id);
          wake();
          renderAll();
        }
      } catch {
        await new Promise((r) => setTimeout(r, 3000));
      }
    }
  }

  function hubText() { hubSub.textContent = (providers?.gateway?.url || "").replace(/^https?:\/\//, "") || t("gateway"); }

  // labels in the page's language, and again when it changes
  function words() {
    for (const s of stats.children) s.lastChild.textContent = t(s.dataset.label);
    hubText();
    if (loaded) { if (cur) { stageFor(cur); renderAll(); } else empty(); }
  }
  new MutationObserver(words).observe(document.documentElement, { attributes: true, attributeFilter: ["lang"] });
  new ResizeObserver(() => layout()).observe(stage);
  words();
  requestAnimationFrame(frame);
  poll();
})();
