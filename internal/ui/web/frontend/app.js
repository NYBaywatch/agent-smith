/* Agent Smith — mobile-style dashboard front end (vanilla JS, no build step).
   Talks to the Go side through window.go.web.App and the "snapshot" /
   "incident" runtime events; falls back to a mock when opened in a browser. */
(() => {
  'use strict';

  // ---------- helpers ----------
  const $ = (sel, root = document) => root.querySelector(sel);
  const $$ = (sel, root = document) => Array.from(root.querySelectorAll(sel));
  const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const ms = (v, dash = '—') => (v == null || !(v > 0)) ? dash : v < 1 ? '<1 ms' : v < 10 ? `${v.toFixed(1)} ms` : `${Math.round(v)} ms`;
  const msN = (v) => (v == null || !(v > 0)) ? '—' : v < 10 ? v.toFixed(1) : String(Math.round(v));
  const pct = (v, d = 1) => (v == null) ? '—' : `${(+v).toFixed(d)}%`;
  const num = (v, d = 0) => (v == null) ? '—' : (+v).toFixed(d);
  const clock = (iso) => iso ? new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit', second: '2-digit' }) : '';
  const hm = (iso) => iso ? new Date(iso).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '';
  const day = (iso) => iso ? new Date(iso).toLocaleDateString([], { month: 'short', day: 'numeric' }) : '';
  const dur = (s) => { s = Math.max(0, Math.round(s || 0)); if (s < 60) return `${s}s`; if (s < 3600) return `${Math.floor(s / 60)}m ${String(s % 60).padStart(2, '0')}s`; return `${Math.floor(s / 3600)}h ${String(Math.floor((s % 3600) / 60)).padStart(2, '0')}m`; };
  const ratingClass = (r) => ({ Excellent: 'r-excellent', Good: 'r-good', Fair: 'r-fair', Poor: 'r-poor' }[r] || 'r-none');
  const sevClass = (n) => ({ OK: 'sev-ok', Watch: 'sev-watch', Degraded: 'sev-degraded', Critical: 'sev-critical' }[n] || 'sev-unknown');
  const sevWord = (n) => ({ OK: 'Operational', Watch: 'Watch', Degraded: 'Degraded', Critical: 'Critical' }[n] || 'Starting');

  // Replace a container's HTML only when it changed — keeps hover/scroll stable on 1 s ticks.
  const sigs = new WeakMap();
  const setHTML = (el, html) => { if (!el) return; if (sigs.get(el) === html) return; sigs.set(el, html); el.innerHTML = html; };

  // ---------- backend bridge ----------
  const bridge = {
    has: () => !!(window.go && window.go.web && window.go.web.App),
    call(name, ...args) {
      if (this.has() && typeof window.go.web.App[name] === 'function') return window.go.web.App[name](...args);
      if (mock[name]) return Promise.resolve(mock[name](...args));
      return Promise.resolve(null);
    },
    on(ev, cb) { if (window.runtime && window.runtime.EventsOn) window.runtime.EventsOn(ev, cb); },
  };

  // ---------- state ----------
  const S = {
    snap: null, history: [], issues: [], incidents: [], info: null,
    screen: 'home', rangeSec: 1200, pathIdx: 0, busyBB: false, busyChecks: false, busyTrace: false,
    lastBB: null,
  };

  // ---------- screens skeletons (cards are stable containers, updated in place) ----------
  const screens = {
    home: ['banner', 'hero', 'tiles', 'tests', 'chart', 'path', 'system', 'connection', 'dns'],
    services: ['svc-head', 'svc-list'],
    route: ['rt-head', 'rt-diag', 'rt-ladder', 'rt-changes', 'rt-trace'],
    insights: ['in-slo', 'in-sla', 'in-bgp', 'in-auth', 'in-alerts'],
    events: ['ev-incidents', 'ev-list', 'ev-actions'],
  };
  const spanCards = new Set(['banner', 'hero', 'tests', 'chart', 'svc-list', 'rt-ladder', 'in-sla', 'ev-list']);
  for (const [name, cards] of Object.entries(screens)) {
    const sec = $(`.screen[data-screen="${name}"]`);
    for (const id of cards) {
      const d = document.createElement('div');
      d.id = `c-${id}`;
      if (spanCards.has(id)) d.classList.add('span');
      sec.appendChild(d);
    }
  }
  const card = (id) => $(`#c-${id}`);

  // ---------- HOME ----------
  function renderHome() {
    const s = S.snap;
    if (!s) return;
    const v = s.verdict || {};
    const sev = sevClass(v.severity_name);

    // Incident banner
    setHTML(card('banner'), s.incident ? `
      <div class="banner">
        <span class="dot"></span>
        <div><b>Incident #${s.incident.id} · ${esc(s.incident.culprit)}</b>
        <span class="t">since ${clock(s.incident.start)} · ${dur(s.incident.duration_s)} · ${s.incident.ticks} degraded readings</span></div>
      </div>` : '');

    // Hero: status ring shows the 24 h error budget left for the internet ring.
    const inet = (s.sla || []).find((e) => e.key === 'internet');
    const budget = inet ? Math.max(0, Math.min(100, inet.budget_left_pct)) : 100;
    const C = 2 * Math.PI * 30;
    const bb = s.headline && s.headline.bufferbloat;
    setHTML(card('hero'), `
      <div class="card ${sev}">
        <div class="hero">
          <svg class="hero-ring" viewBox="0 0 72 72" aria-label="Error budget left ${Math.round(budget)} percent">
            <circle class="track" cx="36" cy="36" r="30"></circle>
            <circle class="arc" cx="36" cy="36" r="30" stroke-dasharray="${C.toFixed(1)}" stroke-dashoffset="${(C * (1 - budget / 100)).toFixed(1)}"></circle>
            <text x="36" y="40" text-anchor="middle">${Math.round(budget)}%</text>
            <title>${Math.round(budget)}% of the 24 h error budget left</title>
          </svg>
          <div>
            <div class="hero-word">${esc(sevWord(v.severity_name))}</div>
            <h1>${esc(v.headline || 'Starting Agent Smith…')}</h1>
            <div class="detail">${esc(v.detail || '')}</div>
          </div>
        </div>
        ${v.fix && v.culprit !== 'Healthy' ? `<div class="chip fix"><b>Fix</b>&nbsp;${esc(v.fix)}</div>` : ''}
        <div class="hero-actions">
          <button class="btn primary" id="btn-bb">
            <svg viewBox="0 0 24 24"><path d="M12 3v9l5 3"/><circle cx="12" cy="12" r="9"/></svg>
            Bufferbloat test
          </button>
          ${bb ? `<span class="chip ${gradeClass(bb.grade)}">Grade <b>${esc(bb.grade)}</b> · +${msN(bb.added_ms)} ms under load · ${num(bb.down_mbps)} Mbps</span>` : `<span class="chip">${S.busyBB ? 'Saturating the link…' : 'Latency under load not measured yet'}</span>`}
        </div>
      </div>`);

    // Tiles
    const h = s.headline || {};
    const spark = sparkline(S.history.map((p) => p.net).filter((x) => x > 0).slice(-40));
    const typical = inet && inet.baseline_valid ? `typical ${msN(inet.baseline_ms)} ms` : 'building your baseline';
    const anomaly = inet && inet.anomalous ? `<span class="chip warn">above baseline</span>` : '';
    setHTML(card('tiles'), `
      <div class="tiles">
        <div class="tile ${ratingClass(h.latency_rating)}">
          <span class="caption">Latency</span>
          <span class="value">${msN(h.latency_ms)}<small>ms</small></span>
          <span class="rating">${esc(h.latency_rating || '—')} ${anomaly}</span>
          ${spark}
          <span class="foot">${typical}</span>
        </div>
        <div class="tile ${ratingClass(h.jitter_rating)}">
          <span class="caption">Jitter</span>
          <span class="value">${msN(h.jitter_ms)}<small>ms</small></span>
          <span class="rating">${esc(h.jitter_rating || '—')}</span>
          <span class="foot">RFC 3550 timing variation</span>
        </div>
        <div class="tile ${ratingClass(h.loss_rating)}">
          <span class="caption">Packet loss</span>
          <span class="value">${h.loss_pct == null ? '—' : h.loss_pct.toFixed(1)}<small>%</small></span>
          <span class="rating">${esc(h.loss_rating || '—')}</span>
          <span class="foot">rolling 15-probe window</span>
        </div>
        <div class="tile ${bb ? gradeRating(bb.grade) : 'r-none'}">
          <span class="caption">Bufferbloat</span>
          <span class="value">${bb ? esc(bb.grade) : '—'}</span>
          <span class="rating">${bb ? `+${msN(bb.added_ms)} ms under load` : 'run the test'}</span>
          <span class="foot">${bb ? `idle ${msN(bb.idle_ms)} → loaded ${msN(bb.loaded_ms)} ms` : 'A/B is healthy for real-time'}</span>
        </div>
      </div>`);

    if (window.renderTestsCard) window.renderTestsCard();

    // Chart card (chart body is drawn separately so hover state survives ticks)
    if (!card('chart').firstChild) {
      card('chart').innerHTML = `
        <div class="card">
          <div class="card-head"><h2>Round-trip time</h2>
            <div class="range" role="group" aria-label="Time range">
              <button data-range="300">5m</button><button data-range="1200" class="is-active">20m</button><button data-range="3600">1h</button>
            </div>
          </div>
          <div class="chart" id="rtt-chart"><svg></svg><div class="tip" id="rtt-tip"></div></div>
          <div class="legend"><span class="net">Internet</span><span class="isp">ISP hop</span><span class="lan">LAN</span></div>
        </div>`;
      $$('.range button', card('chart')).forEach((b) => b.addEventListener('click', () => {
        S.rangeSec = +b.dataset.range;
        $$('.range button', card('chart')).forEach((x) => x.classList.toggle('is-active', x === b));
        drawChart();
      }));
      const ch = $('#rtt-chart');
      ch.addEventListener('pointermove', chartHover);
      ch.addEventListener('pointerleave', () => { chart.hoverX = null; drawChart(); });
    }
    drawChart();

    // Path rings
    setHTML(card('path'), `
      <div class="card">
        <div class="card-head"><h2>Path</h2><span class="sub">you → internet</span></div>
        <div class="list">${(s.rings || []).map((r) => `
          <div class="row">
            <span class="dot ${r.ring.toLowerCase()}"></span>
            <div class="main"><b>${esc(r.name)} <span class="muted" style="display:inline">${esc(r.host)}</span></b>
              <span>${r.alive ? `p95 ${ms(r.p95_ms)} · jitter ${ms(r.jitter_ms)} · loss ${pct(r.loss_pct)}` : 'no reply'}</span></div>
            <div class="val ${ratingClass(r.rating)}" style="color:var(--rating)">${r.alive ? ms(r.mean_ms) : '—'}<small>${esc(r.ring)}</small></div>
          </div>`).join('') || '<div class="empty">Discovering the path…</div>'}
        </div>
      </div>`);

    // System
    const sy = s.sys || {};
    const n = s.net;
    const meter = (label, v, extra = '') => `
      <div class="meter ${v >= 90 ? 'bad' : v >= 75 ? 'warn' : ''}">
        <span class="text-2">${label}</span>
        <div class="track"><div class="fill" style="width:${v < 0 ? 0 : Math.min(100, v)}%"></div></div>
        <span class="v num">${v < 0 ? 'n/a' : `${Math.round(v)}%`}</span>
      </div>${extra}`;
    setHTML(card('system'), `
      <div class="card">
        <div class="card-head"><h2>This PC</h2><span class="sub">${n ? esc(n.media) : ''}</span></div>
        ${meter('CPU', sy.cpu_pct)}
        ${meter('Memory', sy.mem_pct)}
        ${meter('GPU', sy.gpu_pct)}
        <div class="stat-row">
          <span class="chip">↓ <b class="num">${num(sy.in_mbps, 1)}</b> Mbps</span>
          <span class="chip">↑ <b class="num">${num(sy.out_mbps, 1)}</b> Mbps</span>
          ${sy.mem_total_gb ? `<span class="chip"><b class="num">${num(sy.mem_used_gb, 1)}</b> / ${num(sy.mem_total_gb, 1)} GB</span>` : ''}
        </div>
        ${n ? `<dl class="kv">
          <dt>Interface</dt><dd>${esc(n.interface)} · ${n.link_mbps} Mbps · MTU ${n.mtu}${n.errors ? ` · <span style="color:var(--warn)">${n.errors} errors</span>` : ''}</dd>
          ${n.wifi ? `<dt>Wi-Fi</dt><dd>${esc(n.wifi.ssid)} · ${n.wifi.rssi} dBm (${n.wifi.quality}%) · rx ${Math.round(n.wifi.rx_mbps)} / tx ${Math.round(n.wifi.tx_mbps)} Mbps</dd>` : ''}
          <dt>Gateway</dt><dd class="mono">${esc(n.gateway || '—')}</dd>
        </dl>` : ''}
      </div>`);

    // Connection
    const c = s.connection;
    const b = s.bgp;
    setHTML(card('connection'), `
      <div class="card">
        <div class="card-head"><h2>Connection</h2><span class="sub">${c ? esc(c.type) : 'looking up…'}</span></div>
        ${c ? `<dl class="kv">
          <dt>ISP</dt><dd>${esc(c.isp)}${c.org ? ` <span class="muted">· ${esc(c.org)}</span>` : ''}</dd>
          <dt>Public IP</dt><dd class="mono">${esc(c.ip)}</dd>
          <dt>Network</dt><dd class="mono">${esc(c.asn)}${c.as_name ? ` ${esc(c.as_name)}` : ''}</dd>
          <dt>Location</dt><dd>${esc(c.location || '—')}</dd>
          <dt>Reverse DNS</dt><dd class="mono">${esc(c.reverse || '—')}</dd>
          ${c.support ? `<dt>Support</dt><dd><b class="num" style="color:var(--good)">${esc(c.support)}</b></dd>` : ''}
          ${c.support_url ? `<dt>Outage page</dt><dd><a href="#" data-url="${esc(c.support_url)}">${esc(c.support_url.replace(/^https?:\/\//, ''))}</a></dd>` : ''}
          ${b ? `<dt>BGP</dt><dd>${b.announced ? `${esc(b.prefix)} via AS${b.origin_asn} · <span style="color:var(${b.healthy ? '--good' : '--warn'})">seen by ${Math.round(b.visibility_pct)}% of route collectors</span>` : '<span style="color:var(--bad)">prefix not visible in BGP</span>'}</dd>` : ''}
        </dl>` : '<div class="empty">Looking up your ISP and public address…</div>'}
      </div>`);

    // DNS
    const d = s.dns || {};
    setHTML(card('dns'), `
      <div class="card">
        <div class="card-head"><h2>DNS</h2><span class="sub">${d.lookups ? `${ms(d.avg_ms)} avg` : ''}</span></div>
        <div class="list">${(d.servers || []).map((r) => `
          <div class="row">
            <span class="dot" style="--c:${r.ok ? (r.slow ? 'var(--warn)' : 'var(--good)') : 'var(--bad)'}"></span>
            <div class="main"><b>${esc(r.name)}</b><span>${esc(r.addr || 'system resolver')}</span></div>
            <div class="val">${r.ok ? ms(r.avg_ms) : 'fail'}<small>${r.ok ? (r.slow ? 'slow' : 'ok') : ''}</small></div>
          </div>`).join('') || '<div class="empty">Measuring resolvers…</div>'}
        </div>
      </div>`);
  }

  const gradeClass = (g) => ({ 'A+': 'good', A: 'good', B: 'good', C: 'warn' }[g] || 'bad');
  const gradeRating = (g) => ({ 'A+': 'r-excellent', A: 'r-excellent', B: 'r-good', C: 'r-fair' }[g] || 'r-poor');

  function sparkline(vals) {
    if (vals.length < 2) return '<svg class="spark" viewBox="0 0 100 28" preserveAspectRatio="none"></svg>';
    const max = Math.max(...vals) * 1.15 || 1, W = 100, H = 28;
    const pts = vals.map((v, i) => [i * W / (vals.length - 1), H - 2 - (v / max) * (H - 4)]);
    const line = pts.map((p, i) => `${i ? 'L' : 'M'}${p[0].toFixed(1)} ${p[1].toFixed(1)}`).join('');
    const last = pts[pts.length - 1];
    return `<svg class="spark" viewBox="0 0 100 28" preserveAspectRatio="none" aria-hidden="true">
      <path class="area" d="${line}L${W} ${H}L0 ${H}Z"></path><path class="line" d="${line}"></path></svg>`;
  }

  // ---------- RTT chart ----------
  const chart = { hoverX: null };
  function chartData() {
    const cutoff = Date.now() - S.rangeSec * 1000;
    return S.history.filter((p) => new Date(p.t).getTime() >= cutoff);
  }
  function drawChart() {
    const host = $('#rtt-chart');
    if (!host) return;
    const svg = $('svg', host);
    const W = host.clientWidth || 380, H = host.clientHeight || 170;
    const data = chartData();
    if (data.length < 2) { svg.innerHTML = `<text x="${W / 2}" y="${H / 2}" text-anchor="middle" fill="#6f7885" font-size="12">Collecting samples…</text>`; return; }
    const padL = 8, padR = 44, padT = 10, padB = 20;
    const pw = W - padL - padR, ph = H - padT - padB;
    let max = 20;
    for (const p of data) max = Math.max(max, p.net, p.isp, p.gw);
    max = niceMax(max * 1.15);
    const t0 = new Date(data[0].t).getTime(), t1 = new Date(data[data.length - 1].t).getTime() || t0 + 1;
    const x = (t) => padL + ((t - t0) / Math.max(1, t1 - t0)) * pw;
    const y = (v) => padT + ph - (Math.min(v, max) / max) * ph;
    const series = (key) => {
      let d = '', last = 0, started = false;
      for (const p of data) { const v = p[key] > 0 ? p[key] : last; last = v; if (v <= 0) continue; d += `${started ? 'L' : 'M'}${x(new Date(p.t).getTime()).toFixed(1)} ${y(v).toFixed(1)}`; started = true; }
      return d;
    };
    const net = series('net'), isp = series('isp'), gw = series('gw');
    const gridLines = [0, 0.5, 1].map((f) => { const yy = padT + ph * (1 - f); return `<line x1="${padL}" x2="${padL + pw}" y1="${yy}" y2="${yy}"/><text x="${W - 2}" y="${yy + 3}" text-anchor="end" class="axis">${Math.round(max * f)} ms</text>`; }).join('');
    const ticks = [0, 0.5, 1].map((f) => { const t = t0 + (t1 - t0) * f; return `<text x="${x(t)}" y="${H - 4}" text-anchor="${f === 0 ? 'start' : f === 1 ? 'end' : 'middle'}">${new Date(t).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}</text>`; }).join('');
    let cross = '';
    if (chart.hoverX != null) {
      const i = nearestIndex(data, chart.hoverX, x);
      const p = data[i], px = x(new Date(p.t).getTime());
      cross = `<line class="cross" x1="${px}" x2="${px}" y1="${padT}" y2="${padT + ph}"/>` +
        [['net', p.net, 'var(--s-net)'], ['isp', p.isp, 'var(--s-isp)'], ['gw', p.gw, 'var(--s-lan)']].filter((r) => r[1] > 0)
          .map((r) => `<circle class="mark" cx="${px}" cy="${y(r[1])}" r="4" fill="${r[2]}"/>`).join('');
      const tip = $('#rtt-tip');
      tip.classList.add('on');
      tip.innerHTML = `<div class="t">${clock(p.t)}</div>` +
        [['Internet', p.net, 'var(--s-net)'], ['ISP hop', p.isp, 'var(--s-isp)'], ['LAN', p.gw, 'var(--s-lan)']]
          .map((r) => `<div class="r"><span><i style="--c:${r[2]}"></i>${r[0]}</span><b>${ms(r[1])}</b></div>`).join('');
      tip.style.left = `${Math.min(W - 130, Math.max(0, px + 10))}px`;
    } else {
      $('#rtt-tip').classList.remove('on');
    }
    svg.setAttribute('viewBox', `0 0 ${W} ${H}`);
    svg.innerHTML = `<g class="grid">${gridLines}</g><g class="axis">${ticks}</g>
      <path class="area-net" d="${net}L${x(t1).toFixed(1)} ${padT + ph}L${x(t0).toFixed(1)} ${padT + ph}Z"/>
      <path class="l l-lan" d="${gw}"/><path class="l l-isp" d="${isp}"/><path class="l l-net" d="${net}"/>${cross}`;
    chart.geom = { x, data };
  }
  function niceMax(v) { const p = Math.pow(10, Math.floor(Math.log10(v))); const m = v / p; const n = m <= 1 ? 1 : m <= 2 ? 2 : m <= 2.5 ? 2.5 : m <= 5 ? 5 : 10; return n * p; }
  function nearestIndex(data, px, x) {
    let best = 0, bd = Infinity;
    for (let i = 0; i < data.length; i++) { const d = Math.abs(x(new Date(data[i].t).getTime()) - px); if (d < bd) { bd = d; best = i; } }
    return best;
  }
  function chartHover(e) {
    const r = e.currentTarget.getBoundingClientRect();
    chart.hoverX = e.clientX - r.left;
    drawChart();
  }

  // ---------- SERVICES ----------
  function renderServices() {
    const s = S.snap; if (!s) return;
    const list = s.services || [];
    const up = list.filter((x) => x.status === 'ok' || x.status === 'slow').length;
    const down = list.filter((x) => x.status === 'down' || x.status === 'fail').length;
    const pending = list.filter((x) => x.status === 'pending').length;
    const last = list.map((x) => x.last_at).filter(Boolean).sort().pop();
    setHTML(card('svc-head'), `
      <div class="card">
        <div class="card-head"><h2>Services</h2><span class="sub">${last ? `checked ${clock(last)}` : 'first run pending'}</span></div>
        <div class="stat-row">
          <span class="chip good"><b>${up}</b> up</span>
          <span class="chip ${down ? 'bad' : ''}"><b>${down}</b> down</span>
          ${pending ? `<span class="chip"><b>${pending}</b> pending</span>` : ''}
          <span style="flex:1"></span>
          <button class="btn" id="btn-checks" ${S.busyChecks ? 'disabled' : ''}>
            <svg viewBox="0 0 24 24"><path d="M4 12a8 8 0 0 1 14-5l2 2M20 4v5h-5M20 12a8 8 0 0 1-14 5l-2-2M4 20v-5h5"/></svg>${S.busyChecks ? 'Checking…' : 'Check now'}</button>
        </div>
        <p class="text-2" style="margin:0;font-size:12px">Real HTTP requests from this PC every minute — SaaS, cloud, CDN and AI endpoints. Tap one for the timing breakdown.</p>
      </div>`);
    const cats = [];
    for (const x of list) if (!cats.includes(x.category)) cats.push(x.category);
    setHTML(card('svc-list'), `
      <div class="card">
        ${list.length ? cats.map((cat) => `
          <div class="caption group-title">${esc(cat)}</div>
          <div class="list">${list.filter((x) => x.category === cat).map((x, i) => svcRow(x)).join('')}</div>`).join('')
          : '<div class="empty">No synthetic checks configured — add some in config.json.</div>'}
      </div>`);
  }
  function svcRow(x) {
    const val = x.status === 'pending' ? '<span class="muted">pending</span>' : x.status === 'ok' || x.status === 'slow' ? `${ms(x.ttfb_ms)}<small>TTFB · ${ms(x.total_ms)} total</small>` : `<span style="color:var(--bad)">${esc(x.error || (x.http_status ? `HTTP ${x.http_status}` : 'failed'))}</span>`;
    return `<div class="row tap" tabindex="0" data-svc="${esc(x.url)}">
      <span class="pill ${esc(x.status)}">${esc(x.status)}</span>
      <div class="main"><b>${esc(x.name)}</b><span>${x.edge ? esc(x.edge) : esc(x.provider || x.url.replace(/^https?:\/\//, ''))}${x.runs ? ` · ${Math.round(x.avail_pct)}% up` : ''}</span></div>
      <div class="val">${val}</div>
    </div>`;
  }
  function serviceSheet(x) {
    const phases = [['DNS', x.dns_ms, 'p1'], ['Connect', x.connect_ms, 'p2'], ['TLS', x.tls_ms, 'p3'], ['Wait (TTFB)', x.ttfb_ms, 'p4'], ['Download', x.download_ms, 'p5']];
    const sum = phases.reduce((a, p) => a + Math.max(0, p[1] || 0), 0) || 1;
    openSheet(x.name, `
      <div class="stat-row">
        <span class="pill ${esc(x.status)}">${esc(x.status)}</span>
        <span class="chip">${esc(x.category)}${x.provider ? ` · ${esc(x.provider)}` : ''}</span>
        ${x.edge ? `<span class="chip accent">edge ${esc(x.edge)}</span>` : ''}
      </div>
      <div>
        <h3>Where the time goes</h3>
        <div class="wf">
          <div class="bar">${phases.map((p) => `<i class="${p[2]}" style="flex:${Math.max(0, p[1] || 0) / sum}" title="${p[0]} ${ms(p[1])}"></i>`).join('')}</div>
          <div class="lg">${phases.map((p) => `<div class="${p[2]}"><b>${msN(p[1])}</b><span>${p[0]}</span></div>`).join('')}</div>
        </div>
        <p style="margin-top:8px">${x.runs === 1 ? 'From the only run so far' : `Averages over the last ${x.runs} runs`}. Total ${ms(x.total_ms)}; a fresh connection each time, so DNS, connect and TLS are always measured.</p>
      </div>
      <dl class="kv">
        <dt>Availability</dt><dd class="num">${pct(x.avail_pct, 1)} of ${x.runs} runs</dd>
        <dt>Last status</dt><dd>${x.http_status ? `HTTP ${x.http_status}` : '—'}${x.error ? ` · <span style="color:var(--bad)">${esc(x.error)}</span>` : ''}</dd>
        <dt>Last run</dt><dd>${x.last_at ? `${day(x.last_at)} ${clock(x.last_at)}` : '—'}</dd>
        <dt>URL</dt><dd class="mono"><a href="#" data-url="${esc(x.url)}">${esc(x.url)}</a></dd>
      </dl>`);
  }

  // ---------- ROUTE ----------
  function renderRoute() {
    const s = S.snap; if (!s) return;
    const paths = s.paths || [];
    if (S.pathIdx >= paths.length) S.pathIdx = 0;
    const p = paths[S.pathIdx];
    setHTML(card('rt-head'), `
      <div class="card">
        <div class="card-head"><h2>Route</h2><span class="sub">${p ? `traced ${clock(p.when)}` : ''}</span></div>
        ${paths.length ? `<div class="chips">${paths.map((q, i) => `<button data-path="${i}" class="${i === S.pathIdx ? 'is-active' : ''}">${esc(q.name)}</button>`).join('')}</div>` : ''}
        ${p ? `<div class="stat-row">
          <span class="chip"><b>${p.hops.length}</b> hops</span>
          <span class="chip ${p.reached ? 'good' : 'bad'}">${p.reached ? 'reached' : 'not reached'}</span>
          ${p.as_path ? `<span class="chip mono">${esc(p.as_path)}</span>` : ''}
        </div>` : '<div class="empty">The first trace runs a few seconds after start.</div>'}
      </div>`);
    if (!p) { setHTML(card('rt-diag'), ''); setHTML(card('rt-ladder'), ''); }
    else {
      const d = p.diagnosis || { hop_index: -1 };
      const bad = d.hop_index >= 0 && p.hops[d.hop_index];
      setHTML(card('rt-diag'), bad ? `
        <div class="diag deg"><span>⚠</span><div><span class="k">${esc(d.reason)}</span> at hop ${bad.ttl} (${esc(bad.addr)}), in the ${esc(d.segment)} segment. That is where the trouble starts.</div></div>`
        : `<div class="diag clean"><span>✓</span><div><span class="k">Path is clean.</span> No hop introduces persistent loss or latency; loss that vanishes downstream is just routers de-prioritising ping.</div></div>`);
      setHTML(card('rt-ladder'), `
        <div class="card">
          <div class="ladder">${p.hops.map((h, i) => `
            <div class="hop ${h.degraded ? 'deg' : ''} ${h.addr ? '' : 'silent'} ${p.reached && i === p.hops.length - 1 ? 'dest' : ''}" data-hop="${i}" tabindex="0">
              <span class="n">${h.ttl}</span>
              <div class="a"><b>${h.addr ? esc(h.addr) : '* no reply'}</b><span>${hopWho(h)}</span></div>
              <div class="m">${h.addr ? ms(h.avg_ms) : ''}<small class="${h.loss_pct > 0 ? 'loss' : ''} ${h.loss_pct >= 50 ? 'hi' : ''}">${h.addr ? (h.loss_pct > 0 ? `${Math.round(h.loss_pct)}% loss` : esc(h.segment)) : ''}</small></div>
            </div>`).join('')}
          </div>
        </div>`);
    }
    const rc = s.route_changes || [];
    setHTML(card('rt-changes'), `
      <div class="card">
        <div class="card-head"><h2>Route changes</h2><span class="sub">${rc.length ? `${rc.length} recorded` : 'none yet'}</span></div>
        ${rc.length ? `<div class="list">${rc.slice(0, 6).map((c) => `
          <div class="row"><span class="dot" style="--c:var(--warn)"></span>
            <div class="main"><b>${esc(c.name)} · ${c.hops_before} → ${c.hops_after} hops</b><span>${esc(c.as_before || '—')} → ${esc(c.as_after || '—')}</span></div>
            <div class="val">${clock(c.when)}<small>${day(c.when)}</small></div></div>`).join('')}</div>`
          : '<p class="text-2" style="margin:0;font-size:12px">Every anchor is re-traced every few minutes; a different set of routers means your ISP or its peers changed the path.</p>'}
      </div>`);
    if (!card('rt-trace').firstChild) {
      card('rt-trace').innerHTML = `
        <div class="card">
          <div class="card-head"><h2>Trace a host</h2></div>
          <form class="field" id="trace-form"><input id="trace-host" placeholder="game server, api.example.com, 8.8.8.8" autocomplete="off"><button class="btn primary" type="submit">Trace</button></form>
        </div>`;
      $('#trace-form').addEventListener('submit', async (e) => {
        e.preventDefault();
        const host = $('#trace-host').value.trim();
        if (!host || S.busyTrace) return;
        S.busyTrace = true;
        const btn = $('button', e.currentTarget); btn.disabled = true; btn.textContent = 'Tracing…';
        try {
          const p = await bridge.call('Trace', host);
          if (!p || p.error) toast(p && p.error ? p.error : 'Trace failed', 'bad');
          else traceSheet(p);
        } finally { S.busyTrace = false; btn.disabled = false; btn.textContent = 'Trace'; }
      });
    }
  }
  const hopWho = (h) => h.as_name ? `AS${h.asn} ${esc(h.as_name)}` : h.asn ? `AS${h.asn}` : h.private ? 'private network' : h.host ? esc(h.host) : h.addr ? `${esc(h.segment)} router · no reverse DNS` : 'no response at this TTL';
  function hopSheet(p, i) {
    const h = p.hops[i];
    openSheet(`Hop ${h.ttl}${h.addr ? ` · ${h.addr}` : ''}`, `
      <div class="stat-row"><span class="pill seg">${esc(h.segment)}</span>${h.degraded ? '<span class="pill down">degradation starts here</span>' : ''}${h.private ? '<span class="pill">private address</span>' : ''}</div>
      <dl class="kv">
        <dt>Host</dt><dd class="mono">${esc(h.host || '—')}</dd>
        <dt>Network</dt><dd>${h.asn ? `AS${h.asn} ${esc(h.as_name || '')}` : '—'}</dd>
        <dt>RTT</dt><dd class="num">avg ${ms(h.avg_ms)} · max ${ms(h.max_ms)}</dd>
        <dt>Loss</dt><dd class="num">${Math.round(h.loss_pct)}%</dd>
      </dl>
      <p>${h.loss_pct > 0 && !h.degraded ? 'Loss at this hop that does not persist to later hops is ICMP rate limiting by the router, not real packet loss on your path.' : 'Each TTL is probed 3 times; values are per-hop round trips from this PC.'}</p>`);
  }
  function traceSheet(p) {
    const d = p.diagnosis || { hop_index: -1 };
    openSheet(`Route to ${p.name}`, `
      <div class="stat-row"><span class="chip"><b>${p.hops.length}</b> hops</span><span class="chip ${p.reached ? 'good' : 'bad'}">${p.reached ? 'reached' : 'not reached'}</span>${p.as_path ? `<span class="chip mono">${esc(p.as_path)}</span>` : ''}</div>
      ${d.hop_index >= 0 ? `<div class="diag deg"><span>⚠</span><div>${esc(d.reason)} at hop ${p.hops[d.hop_index].ttl} (${esc(d.segment)} segment)</div></div>` : '<div class="diag clean"><span>✓</span><div>Path is clean.</div></div>'}
      <div class="ladder">${p.hops.map((h, i) => `
        <div class="hop ${h.degraded ? 'deg' : ''} ${h.addr ? '' : 'silent'}">
          <span class="n">${h.ttl}</span>
          <div class="a"><b>${h.addr ? esc(h.addr) : '* no reply'}</b><span>${hopWho(h)}</span></div>
          <div class="m">${h.addr ? ms(h.avg_ms) : ''}<small>${h.addr ? (h.loss_pct > 0 ? `${Math.round(h.loss_pct)}% loss` : esc(h.segment)) : ''}</small></div>
        </div>`).join('')}</div>`);
  }

  // ---------- INSIGHTS ----------
  function renderInsights() {
    const s = S.snap; if (!s) return;
    const slo = s.slo || {};
    const inet = (s.sla || []).find((e) => e.key === 'internet');
    const avail = inet && inet.samples_24h ? inet.avail_24h_pct : null;
    const budget = inet ? inet.budget_left_pct : 100;
    setHTML(card('in-slo'), `
      <div class="card">
        <div class="card-head"><h2>Service level</h2><span class="sub">last 24 h</span></div>
        <div class="hero-num">${avail == null ? '—' : avail.toFixed(avail >= 99.95 ? 3 : 2)}<small>% available</small></div>
        <div class="bar-line">
          <div class="track"><div class="fill ${budget < 25 ? 'bad' : budget < 50 ? 'warn' : ''}" style="width:${Math.max(0, Math.min(100, budget))}%"></div></div>
          <span class="num text-2">${budget < 0 ? 'budget exhausted' : `${Math.round(budget)}% budget left`}</span>
        </div>
        <div class="stat-row">
          <span class="chip">SLO <b>${num(slo.availability_pct, 3)}%</b> up</span>
          ${slo.p95_ms ? `<span class="chip">p95 ≤ <b>${slo.p95_ms} ms</b></span>` : ''}
          ${inet && inet.samples_24h ? `<span class="chip ${inet.slo_ok ? 'good' : 'bad'}">${inet.slo_ok ? 'meeting SLO' : 'breaching SLO'}</span>` : ''}
          ${inet && inet.samples_24h ? `<span class="chip">p95 <b>${msN(inet.p95_24h_ms)} ms</b></span>` : ''}
        </div>
      </div>`);
    const rows = (s.sla || []).filter((e) => e.samples_24h > 0);
    setHTML(card('in-sla'), `
      <div class="card">
        <div class="card-head"><h2>Availability & baselines</h2><span class="sub">24 h</span></div>
        ${rows.length ? `<div class="sla-grid">
          <span class="h">Series</span><span class="h n">up · p95</span><span class="h n">typical</span>
          ${rows.map((e) => `
            <span class="name">${esc(e.name)}<small>${e.kind === 'http' ? 'HTTP check' : 'ping ring'}${e.anomalous ? ' · <span style="color:var(--warn)">above baseline now</span>' : ''}</small></span>
            <span class="n ${e.slo_ok ? '' : 'bad'}">${e.avail_24h_pct.toFixed(2)}% · ${msN(e.p95_24h_ms)}</span>
            <span class="n ${e.anomalous ? 'warn' : ''}">${e.baseline_valid ? `${msN(e.baseline_ms)} ms` : '—'}</span>`).join('')}
        </div>` : '<div class="empty">Baselines appear after about 15 minutes of history.</div>'}
      </div>`);
    const b = s.bgp;
    setHTML(card('in-bgp'), `
      <div class="card">
        <div class="card-head"><h2>BGP reachability</h2><span class="sub">RIPEstat</span></div>
        ${b ? `<div class="bar-line"><div class="track"><div class="fill ${b.healthy ? '' : 'warn'}" style="width:${Math.round(b.visibility_pct)}%"></div></div><span class="num text-2">${Math.round(b.visibility_pct)}%</span></div>
        <p class="text-2" style="margin:0;font-size:12px">${b.announced ? `Your prefix <span class="mono">${esc(b.prefix)}</span> is announced by AS${b.origin_asn} and seen by ${b.visible_peers} of ${b.total_peers} route collectors.` : 'Your address is not covered by any announced prefix — parts of the internet may not be able to reach you.'}</p>`
          : '<div class="empty">Checked hourly once your public IP is known.</div>'}
      </div>`);
    const auth = (s.dns && s.dns.authoritative) || [];
    setHTML(card('in-auth'), `
      <div class="card">
        <div class="card-head"><h2>Authoritative DNS</h2><span class="sub">cache-miss cost</span></div>
        <div class="list">${auth.map((a) => `
          <div class="row"><span class="dot" style="--c:${a.ok ? (a.ms > 250 ? 'var(--warn)' : 'var(--good)') : 'var(--bad)'}"></span>
            <div class="main"><b>${esc(a.domain)}</b><span>${esc((a.ns || '').replace(/\.$/, '') || a.err || '')}</span></div>
            <div class="val">${a.ok ? ms(a.ms) : 'fail'}</div></div>`).join('') || '<div class="empty">Measured every 5 minutes.</div>'}
        </div>
      </div>`);
    const al = s.alerts || {};
    setHTML(card('in-alerts'), `
      <div class="card">
        <div class="card-head"><h2>Alert compression</h2></div>
        <div class="stat-row">
          <span class="chip"><b class="num">${al.raw || 0}</b> degraded readings</span>
          <span class="chip accent">→ <b class="num">${al.incidents || 0}</b> incidents</span>
        </div>
      </div>`);
  }

  // ---------- EVENTS ----------
  function renderEvents() {
    const inc = S.incidents || [];
    const open = inc.filter((i) => i.open).length;
    $('#events-badge').hidden = open === 0;
    setHTML(card('ev-incidents'), `
      <div class="card">
        <div class="card-head"><h2>Incidents</h2><span class="sub">${inc.length ? `${open ? `${open} open · ` : ''}${inc.length} total` : 'none'}</span></div>
        ${inc.length ? `<div class="list">${inc.slice(0, 30).map((i, k) => `
          <div class="ev" data-inc="${k}" tabindex="0" style="--status:${i.open ? 'var(--bad)' : 'var(--muted)'}">
            <span class="stripe"></span>
            <div class="main"><b>#${i.id} ${esc(i.headline)}</b><span>${esc(i.culprit)} · ${esc(i.peak_name)} · ${i.ticks} readings${i.open ? ' · <span style="color:var(--bad)">open</span>' : ''}</span></div>
            <div class="when"><b>${dur(i.duration_s)}</b>${day(i.start)} ${hm(i.start)}</div>
          </div>`).join('')}</div>` : '<div class="empty">No incidents — connection looking clean.</div>'}
      </div>`);
    const list = S.issues || [];
    setHTML(card('ev-list'), `
      <div class="card">
        <div class="card-head"><h2>Events</h2><span class="sub">${list.length ? `${list.length} recorded` : 'none'}</span></div>
        ${list.length ? `<div class="list">${list.slice(0, 60).map((is, k) => `
          <div class="ev ${sevClass(is.severity_name)}" data-issue="${k}" tabindex="0">
            <span class="stripe"></span>
            <div class="main"><b>${esc(is.headline)}</b><span>${esc(is.severity_name)} · ${esc(is.culprit)}</span></div>
            <div class="when"><b>${hm(is.time)}</b>${day(is.time)}</div>
          </div>`).join('')}</div>` : '<div class="empty">No events recorded yet.</div>'}
      </div>`);
    setHTML(card('ev-actions'), `
      <div class="foot-actions">
        <button class="btn ghost" id="btn-clear-inc">Clear incidents</button>
        <button class="btn ghost" id="btn-clear-ev">Clear events</button>
      </div>`);
  }
  function issueSheet(is) {
    openSheet(is.headline, `
      <div class="stat-row"><span class="pill ${esc(is.severity_name.toLowerCase())}">${esc(is.severity_name)}</span><span class="chip">${esc(is.culprit)}</span><span class="chip mono">${day(is.time)} ${clock(is.time)}</span></div>
      <div><h3>What this means</h3><p>${esc(is.summary || is.detail)}</p></div>
      <div><h3>Measurements</h3>${(is.lines || []).map((l) => `<div class="ln"><b>${esc(l.value)}</b><span class="rt ${ratingClass(l.rating[0] + l.rating.slice(1).toLowerCase())}" style="color:var(--rating)">${esc(l.rating)}</span><span class="lab">${esc(l.label)}</span><span class="me">${esc(l.meaning)}</span></div>`).join('')}</div>
      ${is.fix ? `<div><h3>Suggested fix</h3><p>${esc(is.fix)}</p></div>` : ''}
      ${is.procs && is.procs.length ? `<div><h3>Busiest processes at the time</h3><div class="procs">${is.procs.map((p) => `<span>${esc(p.name)}</span><span>${Math.round(p.cpu)}% CPU</span><span>${Math.round(p.mem_mb)} MB</span>`).join('')}</div></div>` : ''}`);
  }
  function incidentSheet(i) {
    openSheet(`Incident #${i.id}`, `
      <div class="stat-row"><span class="pill ${i.open ? 'down' : ''}">${i.open ? 'open' : 'resolved'}</span><span class="chip">${esc(i.culprit)}</span><span class="chip">peak ${esc(i.peak_name)}</span></div>
      <dl class="kv">
        <dt>Started</dt><dd>${day(i.start)} ${clock(i.start)}</dd>
        <dt>${i.open ? 'Duration' : 'Ended'}</dt><dd>${i.open ? dur(i.duration_s) : `${day(i.end)} ${clock(i.end)} · lasted ${dur(i.duration_s)}`}</dd>
        <dt>Readings</dt><dd>${i.ticks} degraded readings folded into this incident</dd>
        ${i.culprits && i.culprits.length > 1 ? `<dt>Evolved</dt><dd>${i.culprits.map(esc).join(' → ')}</dd>` : ''}
      </dl>
      <div><h3>First reading</h3><p>${esc(i.headline)}</p></div>
      ${i.last && i.last !== i.headline ? `<div><h3>Latest reading</h3><p>${esc(i.last)}</p></div>` : ''}
      ${i.fix ? `<div><h3>Suggested fix</h3><p>${esc(i.fix)}</p></div>` : ''}`);
  }

  // ---------- sheet / toast / tabs ----------
  function openSheet(title, bodyHTML) {
    $('#sheet-title').textContent = title;
    $('#sheet-body').innerHTML = bodyHTML;
    $('#sheet').hidden = false; $('#sheet-scrim').hidden = false;
    $('#sheet-close').focus();
  }
  function closeSheet() { $('#sheet').hidden = true; $('#sheet-scrim').hidden = true; }
  function toast(text, kind = '') {
    const t = document.createElement('div');
    t.className = `toast ${kind}`; t.textContent = text;
    $('#toasts').appendChild(t);
    setTimeout(() => t.remove(), 4200);
  }
  function showScreen(name) {
    S.screen = name;
    $$('.screen').forEach((s) => s.classList.toggle('is-active', s.dataset.screen === name));
    $$('.tab').forEach((t) => t.classList.toggle('is-active', t.dataset.tab === name));
    renderAll();
  }
  function renderAll() {
    const r = { home: renderHome, services: renderServices, route: renderRoute, insights: renderInsights, events: renderEvents }[S.screen];
    if (r) r();
    const s = S.snap;
    if (s) {
      const hs = $('#hdr-status');
      hs.textContent = sevWord(s.verdict.severity_name);
      hs.className = `hdr-status ${sevClass(s.verdict.severity_name)}`;
    }
    $('#events-badge').hidden = !(S.incidents || []).some((i) => i.open);
  }

  // ---------- event delegation ----------
  document.addEventListener('click', async (e) => {
    const t = e.target.closest('[data-url], #btn-bb, #btn-checks, #btn-clear-inc, #btn-clear-ev, [data-svc], [data-hop], [data-path], [data-issue], [data-inc], .tab, #btn-min, #btn-hide, #sheet-close, #sheet-scrim');
    if (!t) return;
    if (t.id === 'sheet-close' || t.id === 'sheet-scrim') return closeSheet();
    if (t.classList.contains('tab')) return showScreen(t.dataset.tab);
    if (t.id === 'btn-min') return bridge.call('Minimise');
    if (t.id === 'btn-hide') { toast('Still watching from the tray'); return bridge.call('Hide'); }
    if (t.dataset.url) { e.preventDefault(); return bridge.call('OpenURL', t.dataset.url); }
    if (t.id === 'btn-bb') return; // handled by tests.js
    if (t.id === 'btn-checks') {
      if (S.busyChecks) return;
      S.busyChecks = true; renderServices();
      const list = await bridge.call('RunChecks');
      S.busyChecks = false;
      if (Array.isArray(list) && S.snap) S.snap.services = list;
      renderServices();
      toast('Checks finished');
      return;
    }
    if (t.id === 'btn-clear-inc') { await bridge.call('ClearIncidents'); S.incidents = await bridge.call('Incidents') || []; renderEvents(); return; }
    if (t.id === 'btn-clear-ev') { await bridge.call('ClearIssues'); S.issues = await bridge.call('Issues') || []; renderEvents(); return; }
    if (t.dataset.svc) { const x = (S.snap.services || []).find((s) => s.url === t.dataset.svc); if (x) serviceSheet(x); return; }
    if (t.dataset.path) { S.pathIdx = +t.dataset.path; renderRoute(); return; }
    if (t.dataset.hop) { const p = S.snap.paths[S.pathIdx]; if (p) hopSheet(p, +t.dataset.hop); return; }
    if (t.dataset.issue) { const is = S.issues[+t.dataset.issue]; if (is) issueSheet(is); return; }
    if (t.dataset.inc) { const i = S.incidents[+t.dataset.inc]; if (i) incidentSheet(i); return; }
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeSheet();
    if (e.key === 'Enter' && e.target.matches('[data-svc], [data-hop], [data-issue], [data-inc]')) e.target.click();
  });
  window.addEventListener('resize', () => { if (S.screen === 'home') drawChart(); });

  // ---------- data flow ----------
  let lastListsAt = 0;
  async function refreshLists(force = false) {
    const now = Date.now();
    if (!force && now - lastListsAt < 5000) return;
    lastListsAt = now;
    const [issues, incidents] = await Promise.all([bridge.call('Issues'), bridge.call('Incidents')]);
    S.issues = issues || []; S.incidents = incidents || [];
  }
  async function refreshHistory() { S.history = (await bridge.call('History')) || []; }

  // Splash: stays up at least 1.6 s, leaves once the engine has real readings, never past 6 s.
  const splash = { shownAt: performance.now(), done: false, pinned: false };
  function hideSplash(force) {
    if (splash.done || (splash.pinned && !force)) return;
    const wait = force ? 0 : Math.max(0, 1600 - (performance.now() - splash.shownAt));
    setTimeout(() => { splash.done = true; $('#splash').classList.add('out'); }, wait);
  }
  window.__splash = (on) => { const el = $('#splash'); splash.pinned = on; if (on) { splash.done = false; el.classList.remove('out'); } else hideSplash(true); };
  setTimeout(() => hideSplash(true), 6000);

  async function boot() {
    S.info = await bridge.call('Info');
    S.snap = await bridge.call('Snapshot');
    await Promise.all([refreshHistory(), refreshLists(true)]);
    renderAll();
    bridge.on('snapshot', async (snap) => {
      S.snap = snap;
      if (!splash.done) {
        const st = $('#splash-status');
        if (st) st.textContent = snap.verdict && snap.verdict.headline && snap.rings && snap.rings.length ? snap.verdict.headline : 'Taking the first readings…';
        if (snap.rings && snap.rings.length) hideSplash(false);
      }
      // History grows one point per tick; append locally and resync every 30 s.
      const last = S.history[S.history.length - 1];
      const rings = Object.fromEntries((snap.rings || []).map((r) => [r.ring, r]));
      const pt = { t: snap.time, gw: rings.LAN && rings.LAN.alive ? rings.LAN.mean_ms : 0, isp: rings.ISP && rings.ISP.alive ? rings.ISP.mean_ms : 0, net: snap.headline ? snap.headline.latency_ms : 0 };
      if (!last || last.t !== pt.t) S.history.push(pt);
      if (S.history.length % 30 === 0) refreshHistory();
      if (snap.incident || (S.incidents[0] && S.incidents[0].open)) await refreshLists();
      renderAll();
    });
    bridge.on('incident', async (ev) => {
      if (ev && ev.opened) toast(`Incident: ${ev.opened.headline}`, 'bad');
      if (ev && ev.closed) toast(`Resolved after ${dur(ev.closed.duration_s)}`, 'good');
      await refreshLists(true); renderAll();
    });
    bridge.on('checks-done', () => toast('Service checks finished'));
    bridge.on('bufferbloat-done', (r) => { if (r && r.grade) toast(`Bufferbloat grade ${r.grade}`); });
    if (!bridge.has()) setTimeout(() => hideSplash(false), 1200);
    if (!bridge.has()) setInterval(() => { S.snap = mock.Snapshot(); S.history.push({ t: S.snap.time, gw: 1 + Math.random(), isp: 9 + Math.random() * 3, net: 16 + Math.random() * 6 }); renderAll(); }, 1000);
    setInterval(() => refreshLists(), 20000);
  }

  // ---------- mock (browser preview only) ----------
  const mock = {
    _t0: Date.now(),
    Info: () => ({ version: 'preview', config_path: 'config.json', state_path: 'state.json', started: new Date().toISOString() }),
    Snapshot() {
      const j = (a, b) => a + Math.random() * (b - a);
      return {
        time: new Date().toISOString(),
        verdict: { culprit: 'Healthy', severity: 0, severity_name: 'OK', confidence: 0.9, headline: 'Connection is healthy', detail: 'Internet RTT ~18 ms, jitter 1.2 ms, loss 0.0% (rated Excellent).', fix: '' },
        rings: [{ ring: 'LAN', name: 'Gateway', host: '192.168.1.1', alive: true, mean_ms: j(0.8, 1.4), p95_ms: 1.8, jitter_ms: 0.3, loss_pct: 0, rating: 'Excellent' }, { ring: 'ISP', name: 'ISP hop', host: '67.59.233.44', alive: true, mean_ms: j(9, 12), p95_ms: 14, jitter_ms: 1.1, loss_pct: 0, rating: 'Excellent' }, { ring: 'NET', name: 'Cloudflare', host: '1.1.1.1', alive: true, mean_ms: j(16, 20), p95_ms: 24, jitter_ms: 1.5, loss_pct: 0, rating: 'Excellent' }, { ring: 'NET', name: 'Google', host: '8.8.8.8', alive: true, mean_ms: j(11, 14), p95_ms: 16, jitter_ms: 0.9, loss_pct: 0, rating: 'Excellent' }],
        headline: { latency_ms: j(16, 20), latency_rating: 'Excellent', jitter_ms: 1.4, jitter_rating: 'Excellent', loss_pct: 0, loss_rating: 'Excellent', bufferbloat: null },
        sys: { cpu_pct: j(8, 20), mem_pct: 41, mem_used_gb: 13.1, mem_total_gb: 32, gpu_pct: j(2, 6), in_mbps: j(0.5, 3), out_mbps: j(0.1, 0.5) },
        net: { interface: 'Ethernet', media: 'Wired', link_mbps: 1000, mtu: 1500, errors: 0, gateway: '192.168.1.1', wifi: null },
        dns: { avg_ms: 18, slow: false, lookups: 3, failed: 0, servers: [{ name: 'System (configured)', addr: '', avg_ms: 0.2, ok: true, slow: false }, { name: 'Cloudflare', addr: '1.1.1.1:53', avg_ms: 29, ok: true, slow: false }, { name: 'Google', addr: '8.8.8.8:53', avg_ms: 19, ok: true, slow: false }], authoritative: [{ domain: 'google.com', ns: 'ns1.google.com.', addr: '', ms: 18, ok: true }, { domain: 'github.com', ns: 'ns-421.awsdns-52.com.', addr: '', ms: 12, ok: true }] },
        connection: { ip: '203.0.113.7', isp: 'Example Cable', org: 'Example Online', asn: 'AS64500', as_name: 'EXAMPLE-NET', location: 'Newark, New Jersey, United States', type: 'Fixed line', reverse: 'cpe-203-0-113-7.example.net', support: '1-800-000-0000', support_url: 'https://example.com/outage' },
        bgp: { prefix: '203.0.113.0/24', origin_asn: 64500, visibility_pct: 100, visible_peers: 325, total_peers: 325, announced: true, healthy: true },
        services: ['Cloudflare|CDN|ok|31|112|Cloudflare ORD', 'Fastly|CDN|ok|16|157|Fastly EWR', 'Akamai|CDN|slow|1361|1449|', 'AWS S3 (us-east-1)|Cloud|ok|22|220|CloudFront JFK50', 'Azure|Cloud|ok|77|145|Fastly CHI', 'Microsoft 365|SaaS|ok|22|261|', 'Slack|SaaS|ok|337|406|', 'GitHub API|SaaS|down|0|0|', 'OpenAI API|AI API|ok|85|158|Cloudflare ORD', 'Anthropic API|AI API|ok|54|102|Cloudflare IAD'].map((l) => { const [name, category, status, ttfb, total, edge] = l.split('|'); return { name, category, provider: '', url: `https://${name.toLowerCase().replace(/[^a-z0-9]+/g, '-')}.example/`, status, dns_ms: 15, connect_ms: 12, tls_ms: 30, ttfb_ms: +ttfb, download_ms: Math.max(0, total - ttfb - 57), total_ms: +total, avail_pct: status === 'down' ? 60 : 100, runs: 20, edge, error: status === 'down' ? 'timeout' : '', http_status: status === 'down' ? 0 : 200, last_at: new Date().toISOString() }; }),
        paths: [{ name: 'Cloudflare', dest: '1.1.1.1', when: new Date().toISOString(), reached: true, as_path: 'AS64500>AS13335', elapsed_ms: 4200, hops: [['192.168.1.1', '', 0, 'LAN', 1, 0, true], ['10.240.161.193', '', 0, 'LAN', 8, 0, true], ['67.59.233.44', '', 0, 'ISP', 10, 0], ['67.83.250.128', 'EXAMPLE-NET', 64500, 'ISP', 11, 0], ['63.142.22.160', '', 0, 'Transit', 16, 0], ['173.245.63.162', 'CLOUDFLARENET', 13335, 'Transit', 15, 33], ['1.1.1.1', 'CLOUDFLARENET', 13335, 'Destination', 17, 0]].map((h, i) => ({ ttl: i + 1, addr: h[0], host: '', asn: h[2], as_name: h[1], avg_ms: h[4], max_ms: h[4] + 3, loss_pct: h[5], private: !!h[6], segment: h[3], degraded: false })), diagnosis: { hop_index: -1, reason: '', segment: '' } }],
        route_changes: [],
        sla: [{ key: 'internet', name: 'Internet', kind: 'ping', avail_1h_pct: 100, avail_24h_pct: 99.97, avail_7d_pct: 99.98, p95_24h_ms: 24, mean_24h_ms: 18, samples_24h: 5000, baseline_ms: 18, baseline_valid: true, budget_left_pct: 71, slo_ok: true, availability_ok: true, latency_ok: true, now_ms: 18, z: 0.2, anomalous: false }, { key: 'isp', name: 'ISP hop', kind: 'ping', avail_1h_pct: 100, avail_24h_pct: 100, avail_7d_pct: 100, p95_24h_ms: 14, mean_24h_ms: 10, samples_24h: 5000, baseline_ms: 10, baseline_valid: true, budget_left_pct: 100, slo_ok: true, availability_ok: true, latency_ok: true, now_ms: 10, z: 0, anomalous: false }, { key: 'http:x', name: 'GitHub API', kind: 'http', avail_1h_pct: 60, avail_24h_pct: 97.5, avail_7d_pct: 99.1, p95_24h_ms: 140, mean_24h_ms: 85, samples_24h: 1400, baseline_ms: 80, baseline_valid: true, budget_left_pct: -12, slo_ok: false, availability_ok: false, latency_ok: true, now_ms: 0, z: 0, anomalous: false }],
        slo: { availability_pct: 99.9, p95_ms: 100 },
        incident: null, alerts: { raw: 612, incidents: 4 }, tests: { bufferbloat: null, speed: null, stability: null, dns: null },
      };
    },
    History() { const out = []; const n = 600; for (let i = 0; i < n; i++) { const t = new Date(Date.now() - (n - i) * 1000).toISOString(); out.push({ t, gw: 1 + Math.random() * 0.6, isp: 9 + Math.random() * 3 + (i > 400 && i < 430 ? 20 : 0), net: 16 + Math.random() * 5 + (i > 400 && i < 430 ? 22 : 0) }); } return out; },
    Issues: () => [{ time: new Date(Date.now() - 3600e3).toISOString(), severity: 2, severity_name: 'Degraded', culprit: 'Upstream internet', headline: 'Problem is upstream / on the route to servers', detail: '', fix: 'Try a different server region.', summary: 'Your machine, LAN and ISP edge all look fine — the degradation is upstream.', lines: [{ label: 'Latency', value: '95 ms', rating: 'FAIR', meaning: 'noticeable in games' }], procs: [{ pid: 1, name: 'chrome.exe', cpu: 12, mem_mb: 900 }] }],
    Incidents: () => [{ id: 4, start: new Date(Date.now() - 7200e3).toISOString(), end: new Date(Date.now() - 6900e3).toISOString(), open: false, duration_s: 300, culprit: 'Upstream internet', peak: 2, peak_name: 'Degraded', ticks: 280, headline: 'Problem is upstream / on the route to servers', last: '', fix: '', culprits: ['Upstream internet'] }],
    RunBufferbloat: () => ({ grade: 'A', added_ms: 12, idle_ms: 18, loaded_ms: 30, down_mbps: 412, error: '' }),
    RunChecks: () => mock.Snapshot().services, Trace: (h) => mock.Snapshot().paths[0], ClearIssues: () => null, ClearIncidents: () => null, Minimise: () => null, Hide: () => null, OpenURL: () => null,
  };

  // Shared helpers for the tests module (tests.js).
  window.AS = { S, $, $$, esc, ms, msN, pct, num, clock, day, dur, ratingClass, sevClass, bridge, openSheet, closeSheet, toast, setHTML, card, renderHome };

  boot();
})();
