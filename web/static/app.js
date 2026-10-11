// Temporal autoscaling booth dashboard. No frameworks, no CDNs: one SSE
// stream in, a starfield, a canvas chart, and some DOM diffing.
(() => {
  "use strict";

  const params = new URLSearchParams(location.search);
  const $ = (id) => document.getElementById(id);
  const VERSION_COLORS = {
    aurora: "#1ff1a5", nebula: "#ff6bff", quasar: "#7f86f1", pulsar: "#fecb2f",
    comet: "#59fda0", nova: "#ff5555", zenith: "#c3ff62", stardust: "#b664ff",
  };
  const colorFor = (label) => VERSION_COLORS[label] || "#a5a9f5";
  const PHASE_TEXT = {
    idle: ["Cruising at idle. One pod has this covered.", "Quiet skies. Ziggy's just floating.", "Low traffic: KEDA keeps one warm pod per version."],
    "ramp-up": ["Here comes the traffic! Backlog incoming…", "Slots filling up, KEDA is adding pods!", "Thrusters on: scaling out."],
    peak: ["Peak load! All pods on deck!", "Every slot is busy. Look at those pods!", "Maximum warp!"],
    "ramp-down": ["Traffic easing off…", "Backlog drained. Pods heading home soon.", "Winding down gracefully."],
    cooldown: ["Cooling off. KEDA scales back in.", "Fewer pods, same durable workflows.", "Back to cruising altitude."],
    paused: ["Traffic paused. Ziggy is taking a nap."],
    connecting: ["Warming up the thrusters…"],
  };

  if (params.has("kiosk")) document.body.classList.add("kiosk");

  // ---------------------------------------------------------------- state
  let snap = null;
  let history = [];
  let load = 0; // 0..1, drives starfield warp speed
  const reduceMotion = matchMedia("(prefers-reduced-motion: reduce)").matches;

  // ------------------------------------------------------------- starfield
  const stars = (() => {
    const canvas = $("stars");
    const ctx = canvas.getContext("2d");
    let w = 0, h = 0, dpr = 1, field = [], shooting = null;

    function resize() {
      dpr = Math.min(window.devicePixelRatio || 1, 2);
      w = canvas.clientWidth; h = canvas.clientHeight;
      canvas.width = w * dpr; canvas.height = h * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const count = Math.round((w * h) / 2600);
      field = Array.from({ length: count }, () => newStar(true));
    }
    function newStar(anywhere) {
      const depth = Math.random();
      return {
        x: anywhere ? Math.random() * w : w + 2,
        y: Math.random() * h,
        z: depth,
        r: 0.3 + depth * 1.3,
        tw: Math.random() * Math.PI * 2,
        hue: Math.random() < 0.12 ? (Math.random() < 0.5 ? "203,203,249" : "182,100,255") : "248,250,252",
      };
    }
    let last = performance.now();
    function frame(now) {
      const dt = Math.min(64, now - last) / 1000; last = now;
      ctx.clearRect(0, 0, w, h);
      const warp = 4 + load * 140;
      for (const s of field) {
        s.tw += dt * (1 + s.z * 2);
        if (!reduceMotion) s.x -= warp * (0.15 + s.z) * dt;
        if (s.x < -10) Object.assign(s, newStar(false));
        const a = 0.35 + 0.65 * (0.5 + 0.5 * Math.sin(s.tw)) * (0.4 + s.z * 0.6);
        ctx.fillStyle = `rgba(${s.hue},${a.toFixed(3)})`;
        if (load > 0.5 && s.z > 0.75 && !reduceMotion) {
          const len = Math.min(14, (load - 0.45) * 26 * s.z);
          ctx.fillStyle = `rgba(${s.hue},${(a * 0.55).toFixed(3)})`;
          ctx.fillRect(s.x, s.y - s.r / 2, len, s.r);
        } else {
          ctx.beginPath(); ctx.arc(s.x, s.y, s.r, 0, Math.PI * 2); ctx.fill();
        }
      }
      if (!reduceMotion) {
        if (!shooting && Math.random() < dt * 0.08) {
          shooting = { x: Math.random() * w, y: Math.random() * h * 0.4, vx: -600 - Math.random() * 400, vy: 220, life: 1 };
        }
        if (shooting) {
          const s = shooting;
          s.x += s.vx * dt; s.y += s.vy * dt; s.life -= dt * 1.2;
          const g = ctx.createLinearGradient(s.x, s.y, s.x - s.vx * 0.12, s.y - s.vy * 0.12);
          g.addColorStop(0, `rgba(248,250,252,${Math.max(0, s.life)})`);
          g.addColorStop(1, "rgba(248,250,252,0)");
          ctx.strokeStyle = g; ctx.lineWidth = 1.5;
          ctx.beginPath(); ctx.moveTo(s.x, s.y); ctx.lineTo(s.x - s.vx * 0.12, s.y - s.vy * 0.12); ctx.stroke();
          if (s.life <= 0) shooting = null;
        }
      }
      requestAnimationFrame(frame);
    }
    addEventListener("resize", resize);
    resize();
    requestAnimationFrame(frame);
  })();

  // ----------------------------------------------------------------- chart
  const chart = (() => {
    const canvas = $("chart");
    const ctx = canvas.getContext("2d");
    const WINDOW_MS = 10 * 60 * 1000;
    let w = 0, h = 0;

    function resize() {
      const dpr = Math.min(window.devicePixelRatio || 1, 2);
      w = canvas.clientWidth; h = canvas.clientHeight;
      canvas.width = w * dpr; canvas.height = h * dpr;
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      draw();
    }
    function niceMax(v, min) {
      v = Math.max(v, min);
      const p = Math.pow(10, Math.floor(Math.log10(v)));
      for (const m of [1, 2, 2.5, 5, 10]) if (m * p >= v) return m * p;
      return 10 * p;
    }
    function draw() {
      ctx.clearRect(0, 0, w, h);
      // Scale text, padding, and strokes with the page's fluid root size.
      const rem = parseFloat(getComputedStyle(document.documentElement).fontSize) || 16;
      const k = rem / 16;
      const padL = 3 * rem, padR = 2.6 * rem, padT = 0.7 * rem, padB = 1.7 * rem;
      const cw = w - padL - padR, ch = h - padT - padB;
      if (cw <= 0 || ch <= 0) return;
      const now = history.length ? history[history.length - 1].t : Date.now();
      const t0 = now - WINDOW_MS;
      const pts = history.filter((p) => p.t >= t0);
      const maxRate = niceMax(Math.max(0, ...pts.map((p) => p.rate)), 5);
      const maxPods = niceMax(Math.max(0, ...pts.map((p) => p.pods)), 5);
      const maxBacklog = Math.max(10, ...pts.map((p) => p.backlog));
      const X = (t) => padL + ((t - t0) / WINDOW_MS) * cw;
      const Y = (v, max) => padT + ch - (v / max) * ch;

      // grid + axes
      ctx.font = `${Math.round(0.72 * rem)}px "Noto Sans Mono", monospace`;
      ctx.textBaseline = "middle";
      for (let i = 0; i <= 4; i++) {
        const y = padT + (ch * i) / 4;
        ctx.strokeStyle = "rgba(97,105,237,.16)"; ctx.lineWidth = 1;
        ctx.beginPath(); ctx.moveTo(padL, y); ctx.lineTo(padL + cw, y); ctx.stroke();
        ctx.fillStyle = "#1ff1a5"; ctx.textAlign = "right";
        ctx.fillText(axis(maxRate * (1 - i / 4)), padL - 0.5 * rem, y);
        ctx.fillStyle = "#7f86f1"; ctx.textAlign = "left";
        ctx.fillText(axis(maxPods * (1 - i / 4)), padL + cw + 0.5 * rem, y);
      }
      ctx.fillStyle = "#92a4c3"; ctx.textAlign = "center"; ctx.textBaseline = "top";
      for (let m = 10; m >= 0; m -= 2) ctx.fillText(m ? `-${m}m` : "now", X(now - m * 60000), padT + ch + 0.5 * rem);
      if (pts.length < 2) return;

      // backlog: filled area on its own scale
      ctx.beginPath(); ctx.moveTo(X(pts[0].t), Y(0, 1));
      for (const p of pts) ctx.lineTo(X(p.t), Y(p.backlog / maxBacklog, 1));
      ctx.lineTo(X(pts[pts.length - 1].t), Y(0, 1)); ctx.closePath();
      const g = ctx.createLinearGradient(0, padT, 0, padT + ch);
      g.addColorStop(0, "rgba(255,107,255,.35)"); g.addColorStop(1, "rgba(255,107,255,0)");
      ctx.fillStyle = g; ctx.fill();
      line(pts, (p) => Y(p.backlog / maxBacklog, 1), "rgba(255,107,255,.8)", 1.5 * k);

      // pods: stepped line
      ctx.beginPath();
      pts.forEach((p, i) => {
        const x = X(p.t), y = Y(p.pods, maxPods);
        if (i === 0) ctx.moveTo(x, y); else { ctx.lineTo(x, Y(pts[i - 1].pods, maxPods)); ctx.lineTo(x, y); }
      });
      ctx.strokeStyle = "#7f86f1"; ctx.lineWidth = 3 * k; ctx.shadowColor = "#444ce7"; ctx.shadowBlur = 10; ctx.stroke(); ctx.shadowBlur = 0;

      // target rate
      line(pts, (p) => Y(p.rate, maxRate), "#1ff1a5", 2.5 * k, true);

      // live dot
      const lp = pts[pts.length - 1];
      ctx.fillStyle = "#1ff1a5"; ctx.beginPath(); ctx.arc(X(lp.t), Y(lp.rate, maxRate), 4 * k, 0, Math.PI * 2); ctx.fill();

      function line(points, yf, color, width, glow) {
        ctx.beginPath();
        points.forEach((p, i) => (i ? ctx.lineTo(X(p.t), yf(p)) : ctx.moveTo(X(p.t), yf(p))));
        ctx.strokeStyle = color; ctx.lineWidth = width;
        if (glow) { ctx.shadowColor = color; ctx.shadowBlur = 12; }
        ctx.stroke(); ctx.shadowBlur = 0;
      }
    }
    // The panel resizes with the grid (and the fitter), not just the window.
    new ResizeObserver(resize).observe(canvas.parentElement);
    return { draw };
  })();

  // ------------------------------------------------------------- rendering
  function fmt(n, digits = 0) {
    if (n == null || isNaN(n)) return "0";
    if (n >= 1000) return (n / 1000).toFixed(n >= 10000 ? 0 : 1) + "k";
    return Number(n).toFixed(digits);
  }
  const axis = (v) => (Number.isInteger(v) ? String(v) : String(+v.toFixed(2)));
  const short = (s, n = 10) => (s && s.length > n ? s.slice(-n) : s || "");

  let speechPhase = "";
  let speechTimer = 0;
  function setSpeech(phase) {
    const now = Date.now();
    if (phase === speechPhase && now - speechTimer < 12000) return;
    speechPhase = phase; speechTimer = now;
    const opts = PHASE_TEXT[phase] || PHASE_TEXT.connecting;
    $("speech").textContent = opts[Math.floor(Math.random() * opts.length)];
  }

  function renderHealth(h) {
    document.querySelectorAll("#health .dot").forEach((el) => {
      const ok = !!h[el.dataset.k];
      el.classList.toggle("ok", ok);
      el.classList.toggle("bad", !ok);
    });
  }

  let timelineKey = "";
  function renderCycle(d) {
    const pill = $("phase-pill");
    const phase = d ? d.phase : "connecting";
    pill.textContent = phase.replace("-", " ");
    pill.dataset.phase = phase;
    $("ziggy-stage").dataset.phase = phase;
    $("ziggy-stage").dataset.rollout = String(!!(snap.versions || []).find((v) => v.status === "ramping"));
    setSpeech(phase);
    if (!d) { $("cycle-meta").textContent = "waiting for traffic…"; return; }

    const key = d.phases.map((p) => p.name + p.seconds).join("|");
    const tl = $("timeline");
    if (key !== timelineKey) {
      timelineKey = key;
      tl.querySelectorAll(".seg").forEach((s) => s.remove());
      for (const p of d.phases) {
        const seg = document.createElement("div");
        seg.className = "seg"; seg.dataset.phase = p.name; seg.style.flex = String(p.seconds);
        seg.innerHTML = `<div class="fill"></div><span>${p.name.replace("-", " ")}</span>`;
        tl.insertBefore(seg, $("timeline-marker"));
      }
    }
    let start = 0;
    const elapsed = d.elapsed;
    tl.querySelectorAll(".seg").forEach((seg, i) => {
      const secs = d.phases[i].seconds, end = start + secs;
      seg.classList.toggle("done", elapsed >= end);
      seg.classList.toggle("active", elapsed >= start && elapsed < end);
      seg.querySelector(".fill").style.width = elapsed >= start && elapsed < end ? `${((elapsed - start) / secs) * 100}%` : "";
      start = end;
    });
    // Position the marker inside the active segment using its real geometry.
    let pos = 0, acc = 0;
    tl.querySelectorAll(".seg").forEach((seg, i) => {
      const secs = d.phases[i].seconds;
      if (elapsed >= acc && elapsed < acc + secs) pos = seg.offsetLeft + ((elapsed - acc) / secs) * seg.offsetWidth;
      acc += secs;
    });
    $("timeline-marker").style.transform = `translateX(${pos}px)`;

    const next = d.nextRolloutInCycles;
    const rolloutTxt = next < 0 ? "auto-rollout off" : next === 0 ? "new version rolled out this cycle" : `next rollout in ${next} cycle${next > 1 ? "s" : ""}`;
    const surge = d.surge && d.surge.remainingSeconds > 0 ? ` · ⚡ surge ${Math.round(d.surge.remainingSeconds)}s` : "";
    $("cycle-meta").textContent = `cycle #${d.cycle + 1} · ${fmt(d.totalStarted)} orders started · ${rolloutTxt}${surge}`;
  }

  function renderKPIs(s) {
    const d = s.director;
    const alive = (s.pods || []).filter((p) => !p.terminating);
    const ready = alive.filter((p) => p.ready).length;
    const desired = (s.versions || []).reduce((a, v) => a + (v.desired || 0), 0);
    $("kpi-rate").textContent = fmt(d ? d.targetRate : 0, 1);
    $("kpi-rate-sub").textContent = s.queue.dispatchRate ? `${fmt(s.queue.dispatchRate, 1)} activity tasks/s dispatched` : "incoming traffic";
    $("kpi-backlog").textContent = fmt(s.queue.backlog);
    $("kpi-backlog-sub").textContent = s.queue.backlogAgeSec > 0.5 ? `oldest waiting ${s.queue.backlogAgeSec.toFixed(1)}s` : "waiting in queue";
    $("kpi-running").textContent = fmt(s.runningOrders);
    $("kpi-pods").textContent = String(ready);
    $("kpi-pods-sub").textContent = `${ready} ready / ${desired || alive.length} desired`;
    const util = s.point.util || 0;
    $("kpi-util").textContent = `${Math.round(util * 100)}%`;
    const bar = $("kpi-util-bar");
    bar.style.width = `${Math.min(100, util * 100)}%`;
    bar.classList.toggle("hot", util >= 0.7);
  }

  function renderVersions(s) {
    const el = $("versions");
    const vs = s.versions || [];
    if (!vs.length) { el.innerHTML = `<div class="empty">No worker versions registered yet…</div>`; return; }
    el.innerHTML = vs.map((v) => {
      const pct = v.status === "current" || v.status === "ramping" ? v.rampPercent : 0;
      const statusTxt = v.status === "ramping" ? `ramping ${Math.round(v.rampPercent)}%` : v.status;
      return `<div class="version" style="--vc:${colorFor(v.label)}">
        <div class="version-top">
          <span class="version-name">${esc(v.label)}</span>
          <span class="badge ${esc(v.status)}">${esc(statusTxt)}</span>
        </div>
        <div class="ramp" title="share of new workflows"><div style="width:${pct}%"></div></div>
        <div class="version-stats">
          <span>pods <b>${v.ready}/${v.desired}</b></span>
          <span>backlog <b>${fmt(v.backlog)}</b></span>
          <span>slots <b>${fmt(v.slotsUsed)}/${fmt(v.slotsTotal)}</b></span>
          <span>new traffic <b>${Math.round(pct)}%</b></span>
          <span class="build-id">build ${esc(v.buildId)}</span>
        </div>
      </div>`;
    }).join("");
    const last = s.director && s.director.lastRollout;
    $("rollout-note").textContent = last ? (last.error ? `last rollout failed` : `last rollout: ${last.from} → ${last.to}`) : "";
  }

  // Pod constellation: keyed DOM so pods animate in and out.
  const podEls = new Map();
  let podCount = -1;
  function renderPods(s) {
    const root = $("constellation");
    const groups = new Map();
    for (const v of s.versions || []) groups.set(v.buildId, { label: v.label, status: v.status, pods: [] });
    for (const p of s.pods || []) {
      if (!groups.has(p.buildId)) groups.set(p.buildId, { label: p.label, status: "", pods: [] });
      groups.get(p.buildId).pods.push(p);
    }
    const seen = new Set();
    const wantGroups = [];
    for (const [id, g] of groups) if (g.pods.length) wantGroups.push({ id, ...g });

    const existing = new Map([...root.children].map((c) => [c.dataset.id, c]));
    wantGroups.forEach((g, idx) => {
      let ge = existing.get(g.id);
      if (!ge) {
        ge = document.createElement("div");
        ge.className = "pod-group"; ge.dataset.id = g.id;
        ge.innerHTML = `<div class="pod-group-label"></div><div class="pod-row"></div>`;
      }
      if (root.children[idx] !== ge) root.insertBefore(ge, root.children[idx] || null);
      ge.style.setProperty("--vc", colorFor(g.label));
      const alive = g.pods.filter((p) => !p.terminating).length;
      ge.querySelector(".pod-group-label").textContent = `${g.label} · ${alive} pod${alive === 1 ? "" : "s"}${g.status ? " · " + g.status : ""}`;
      const row = ge.querySelector(".pod-row");
      for (const p of g.pods) {
        seen.add(p.name);
        let pe = podEls.get(p.name);
        if (!pe) {
          pe = document.createElement("div");
          pe.className = "pod enter";
          pe.innerHTML = `<div class="pips">${"<i class='pip'></i>".repeat(4)}</div><div class="pod-name"></div>`;
          podEls.set(p.name, pe);
          setTimeout(() => pe.classList.remove("enter"), 800);
        }
        if (pe.parentElement !== row) row.appendChild(pe);
        pe.style.setProperty("--vc", colorFor(p.label));
        pe.querySelector(".pod-name").textContent = short(p.name, 5);
        pe.classList.toggle("pending", !p.ready && !p.terminating);
        pe.classList.toggle("leaving", p.terminating);
        pe.title = `${p.name}\n${p.phase}${p.ready ? " · ready" : ""}${p.terminating ? " · terminating" : ""}`;
        const total = Math.max(4, Math.round(p.slotsTotal || 4));
        if (pe.querySelectorAll(".pip").length !== total) pe.querySelector(".pips").innerHTML = "<i class='pip'></i>".repeat(total);
        pe.querySelectorAll(".pip").forEach((pip, i) => pip.classList.toggle("on", i < Math.round(p.slotsUsed)));
        pe.classList.toggle("busy", p.slotsTotal > 0 && p.slotsUsed >= p.slotsTotal);
      }
    });
    // Remove vanished pods (fade) and empty groups.
    for (const [name, pe] of podEls) {
      if (!seen.has(name)) {
        podEls.delete(name);
        pe.classList.add("leaving");
        setTimeout(() => pe.remove(), 600);
      }
    }
    const wantIds = new Set(wantGroups.map((g) => g.id));
    for (const ge of [...root.children]) {
      if (ge.dataset.id && !wantIds.has(ge.dataset.id)) setTimeout(() => { if (!ge.querySelector(".pod:not(.leaving)")) ge.remove(); }, 700);
    }
    if (!wantGroups.length && !root.querySelector(".empty")) root.innerHTML = `<div class="empty">No pods yet. Is the cluster up?</div>`;
    else if (wantGroups.length) root.querySelector(".empty")?.remove();

    const n = seen.size + wantGroups.length * 100;
    if (n !== podCount) { podCount = n; requestAnimationFrame(fitPods); }
  }

  // ---------------------------------------------------------- fit to screen
  // CSS scales everything with the window; this handles what's left: if the
  // grid still overflows (short or odd-shaped windows), hide secondary text in
  // steps, then shrink the root size. Pod tiles are sized separately so any
  // number of pods fills (but never overflows) its panel.
  const isPhone = () => matchMedia("(max-width: 600px)").matches;
  function overflowing(el) { return el.scrollHeight > el.clientHeight + 1 || el.scrollWidth > el.clientWidth + 1; }
  function fit() {
    const app = $("app"), rootEl = document.documentElement, body = document.body;
    const rem = () => parseFloat(getComputedStyle(rootEl).fontSize);
    // "Doesn't fit" also covers a chart squeezed too short to read.
    const cramped = () => overflowing(app) || $("chart").parentElement.clientHeight < 12 * rem() || overflowing($("versions"));
    body.classList.remove("fit-1", "fit-2", "fit-3");
    rootEl.style.setProperty("--fit", "1");
    if (!isPhone()) {
      for (const lvl of [1, 2, 3]) {
        if (!cramped()) break;
        body.classList.add(`fit-${lvl}`);
      }
      let f = 1;
      while (overflowing(app) && f > 0.6) {
        f -= 0.04;
        rootEl.style.setProperty("--fit", f.toFixed(2));
      }
    }
    fitPods();
  }
  function fitPods() {
    const c = $("constellation");
    if (isPhone()) { c.style.setProperty("--pod-scale", "1"); return; }
    let scale = 2.2;
    c.style.setProperty("--pod-scale", String(scale));
    while (overflowing(c) && scale > 0.45) {
      scale *= 0.92;
      c.style.setProperty("--pod-scale", scale.toFixed(3));
    }
  }
  let fitTimer = 0;
  const scheduleFit = () => { clearTimeout(fitTimer); fitTimer = setTimeout(fit, 120); };
  addEventListener("resize", scheduleFit);
  document.fonts?.ready.then(scheduleFit);
  let versionCount = -1;

  function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" }[c]));
  }

  function render() {
    if (!snap) return;
    renderHealth(snap.health || {});
    renderCycle(snap.director);
    renderKPIs(snap);
    renderVersions(snap);
    renderPods(snap);
    const vc = (snap.versions || []).length;
    if (vc !== versionCount) { versionCount = vc; scheduleFit(); }
    const d = snap.director;
    const peak = d && d.config ? Math.max(d.config.peakRate, 1) : 10;
    load = d ? Math.min(1, (d.targetRate || 0) / peak) : 0;
    const pauseBtn = $("btn-pause");
    if (pauseBtn && d) { pauseBtn.textContent = d.paused ? "Resume" : "Pause"; pauseBtn.dataset.action = d.paused ? "resume" : "pause"; }
    chart.draw();
  }

  // ------------------------------------------------------------ data feed
  function connect() {
    const es = new EventSource("events");
    es.addEventListener("history", (e) => { history = JSON.parse(e.data) || []; chart.draw(); });
    es.addEventListener("snapshot", (e) => {
      snap = JSON.parse(e.data);
      if (snap.point && (!history.length || history[history.length - 1].t !== snap.point.t)) {
        history.push(snap.point);
        if (history.length > 700) history.shift();
      }
      render();
    });
    es.onerror = () => {
      document.querySelectorAll("#health .dot").forEach((el) => { el.classList.remove("ok"); el.classList.add("bad"); });
    };
  }

  // -------------------------------------------------------------- controls
  function toast(msg) {
    const t = $("toast");
    t.textContent = msg; t.classList.add("show");
    clearTimeout(toast.timer); toast.timer = setTimeout(() => t.classList.remove("show"), 2600);
  }
  if (params.has("controls") && !params.has("kiosk")) {
    const panel = $("controls");
    panel.hidden = false;
    panel.addEventListener("click", async (e) => {
      const btn = e.target.closest("button[data-action]");
      if (!btn) return;
      const action = btn.dataset.action;
      btn.disabled = true;
      try {
        const res = await fetch(`api/${action}`, { method: "POST", headers: { "Content-Type": "application/json" }, body: "{}" });
        const body = await res.json();
        toast(res.ok ? ({ surge: "⚡ Surge! Extra traffic for 60s", pause: "Traffic paused", resume: "Traffic resumed", rollout: "🚀 Rolling out a new version" }[action]) : `Error: ${body.error}`);
      } catch (err) {
        toast(`Error: ${err.message}`);
      } finally {
        btn.disabled = false;
      }
    });
  }

  fetch("api/info").then((r) => r.ok ? r.json() : null).then((info) => {
    if (info && info.temporalUi) $("temporal-ui").href = info.temporalUi;
  }).catch(() => {});

  connect();
})();
