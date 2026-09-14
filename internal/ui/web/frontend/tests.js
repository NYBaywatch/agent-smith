/* On-demand tests: the Tests card on Home, live progress sheets, and result
   sheets for bufferbloat, speed, stability and DNS. Uses the helpers app.js
   exposes on window.AS. */
(() => {
  'use strict';
  const A = window.AS;
  if (!A) return;
  const { $, esc, ms, msN, num, clock, day, dur, ratingClass, bridge, openSheet, setHTML, card, toast } = A;
  const S = A.S;

  const TESTS = {
    speed: { title: 'Speed test', blurb: 'Download and upload throughput, with latency measured under load in each direction.', time: '~20 s', method: 'RunSpeedTest', key: 'speed' },
    bufferbloat: { title: 'Bufferbloat', blurb: 'How much latency piles up when the link is saturated. A or B is healthy for real-time work.', time: '~10 s', method: 'RunBufferbloat', key: 'bufferbloat' },
    stability: { title: 'Stability burst', blurb: '200 probes at 50 ms intervals: loss, jitter, p99 and outage gaps the 1 s monitor smooths over.', time: '~10 s', method: 'RunStability', key: 'stability' },
    dns: { title: 'DNS benchmark', blurb: 'Races your resolver against Cloudflare, Google, Quad9, OpenDNS and the router.', time: '~5 s', method: 'RunDNSBench', key: 'dns' },
  };
  const gradeChip = (g) => `<span class="chip ${({ 'A+': 'good', A: 'good', B: 'good', C: 'warn' }[g] || 'bad')}">grade <b>${esc(g)}</b></span>`;
  const rateClass = (r) => ratingClass(r) ;
  const running = { kind: null, sheetOpen: false };

  // ---- Tests card on Home ----
  function summaryLine(kind, t) {
    const r = t && t[TESTS[kind].key];
    if (!r || r.error) return `<span class="muted">not run yet · ${TESTS[kind].time}</span>`;
    switch (kind) {
      case 'speed': return `<b class="num">${num(r.down_mbps)}</b> ↓ <b class="num">${r.up_measured ? num(r.up_mbps) : '—'}</b> ↑ Mbps · ${r.down_grade}/${r.up_measured ? r.up_grade : '—'} under load · ${day(r.when)} ${clock(r.when)}`;
      case 'bufferbloat': return `grade <b>${esc(r.grade)}</b> · +${msN(r.added_ms)} ms under load · ${num(r.down_mbps)} Mbps`;
      case 'stability': return `<b>${esc(r.rating)}</b> · loss ${r.loss_pct.toFixed(1)}% · jitter ${msN(r.jitter_ms)} ms · p99 ${msN(r.p99_ms)} ms · ${day(r.when)} ${clock(r.when)}`;
      case 'dns': return `fastest <b>${esc(r.fastest)}</b> · ${(r.resolvers || []).length} resolvers · ${day(r.when)} ${clock(r.when)}`;
    }
    return '';
  }
  function ratingOf(kind, t) {
    const r = t && t[TESTS[kind].key];
    if (!r || r.error) return 'r-none';
    switch (kind) {
      case 'speed': return rateClass(r.down_rating);
      case 'bufferbloat': return ({ 'A+': 'r-excellent', A: 'r-excellent', B: 'r-good', C: 'r-fair' }[r.grade] || 'r-poor');
      case 'stability': return rateClass(r.rating);
      case 'dns': { const cur = (r.resolvers || []).find((x) => x.name === r.current); return cur ? rateClass(cur.rating) : 'r-none'; }
    }
    return 'r-none';
  }
  window.renderTestsCard = function renderTestsCard() {
    const t = (S.snap && S.snap.tests) || {};
    setHTML(card('tests'), `
      <div class="card">
        <div class="card-head"><h2>Tests</h2><span class="sub">on demand</span></div>
        <div class="list">${Object.keys(TESTS).map((k) => `
          <div class="row test-row ${ratingOf(k, t)}" data-test="${k}" tabindex="0">
            <span class="dot" style="--c:var(--rating)"></span>
            <div class="main"><b>${TESTS[k].title}</b><span>${summaryLine(k, t)}</span></div>
            <button class="btn" data-run="${k}" ${running.kind ? 'disabled' : ''}>${running.kind === k ? 'Running…' : 'Run'}</button>
          </div>`).join('')}
        </div>
      </div>`);
  };

  // ---- progress sheet ----
  function progressSheet(kind) {
    running.sheetOpen = true;
    openSheet(TESTS[kind].title, `
      <p>${TESTS[kind].blurb}</p>
      <div class="tprog">
        <div class="tprog-big"><span id="tp-val">—</span><small id="tp-unit"></small></div>
        <div class="tprog-phase" id="tp-phase">Starting…</div>
        <div class="bar-line"><div class="track"><div class="fill" id="tp-fill" style="width:2%"></div></div><span class="num text-2" id="tp-pct">0%</span></div>
        <div class="tprog-detail muted" id="tp-detail"></div>
      </div>`);
  }
  function onProgress(p) {
    if (!running.kind || p.test !== running.kind || !running.sheetOpen) return;
    const fill = $('#tp-fill'); if (!fill) return;
    fill.style.width = `${Math.max(2, Math.round((p.pct || 0) * 100))}%`;
    $('#tp-pct').textContent = `${Math.round((p.pct || 0) * 100)}%`;
    $('#tp-detail').textContent = p.detail || '';
    const phase = { idle: 'Measuring idle latency', download: 'Downloading', upload: 'Uploading', load: 'Latency under download', burst: 'Probing', lookup: 'Resolving', done: 'Finishing' }[p.phase] || p.phase;
    $('#tp-phase').textContent = phase;
    if (p.mbps > 0 && (p.phase === 'download' || p.phase === 'upload')) { $('#tp-val').textContent = num(p.mbps, p.mbps < 10 ? 1 : 0); $('#tp-unit').textContent = ' Mbps'; }
    else if (p.rtt_ms > 0) { $('#tp-val').textContent = msN(p.rtt_ms); $('#tp-unit').textContent = ' ms'; }
    else if (p.total) { $('#tp-val').textContent = `${p.done}`; $('#tp-unit').textContent = ` / ${p.total}`; }
  }
  bridge.on('test-progress', onProgress);

  // ---- result sheets ----
  function resultSheet(kind, r) {
    const T = TESTS[kind];
    if (!r || r.error) { openSheet(T.title, `<div class="diag deg"><span>⚠</span><div><span class="k">Test failed.</span> ${esc(r ? r.error : 'no response')}</div></div>`); return; }
    let body = '';
    if (kind === 'speed') {
      body = `
        <div class="tiles">
          <div class="tile ${rateClass(r.down_rating)}"><span class="caption">Download</span><span class="value">${num(r.down_mbps, r.down_mbps < 10 ? 1 : 0)}<small>Mbps</small></span><span class="rating">${esc(r.down_rating)}</span><span class="foot">peak ${num(r.down_peak_mbps)} · ${gradeChip(r.down_grade)}</span></div>
          <div class="tile ${r.up_measured ? rateClass(r.up_rating) : 'r-none'}"><span class="caption">Upload</span><span class="value">${r.up_measured ? num(r.up_mbps, r.up_mbps < 10 ? 1 : 0) : '—'}<small>Mbps</small></span><span class="rating">${r.up_measured ? esc(r.up_rating) : 'not measured'}</span><span class="foot">${r.up_measured ? `peak ${num(r.up_peak_mbps)} · ${gradeChip(r.up_grade)}` : ''}</span></div>
        </div>
        <div><h3>Latency under load</h3>
          <dl class="kv">
            <dt>Idle</dt><dd class="num">${ms(r.idle_ms)}</dd>
            <dt>While downloading</dt><dd class="num">${ms(r.down_rtt_ms)} <span class="muted">(+${msN(r.down_added_ms)} ms)</span></dd>
            ${r.up_measured ? `<dt>While uploading</dt><dd class="num">${ms(r.up_rtt_ms)} <span class="muted">(+${msN(r.up_added_ms)} ms)</span></dd>` : ''}
            <dt>Server</dt><dd>${esc(r.source)}${r.colo ? ` · ${esc(r.colo)}` : ''} · ${num(r.duration_s)} s</dd>
          </dl></div>
        <div><h3>What this means</h3><p>${esc(r.summary)}</p></div>`;
    } else if (kind === 'bufferbloat') {
      body = `
        <div class="tiles">
          <div class="tile ${({ 'A+': 'r-excellent', A: 'r-excellent', B: 'r-good', C: 'r-fair' }[r.grade] || 'r-poor')}"><span class="caption">Grade</span><span class="value">${esc(r.grade)}</span><span class="rating">+${msN(r.added_ms)} ms under load</span></div>
          <div class="tile r-none"><span class="caption">Download while testing</span><span class="value">${num(r.down_mbps)}<small>Mbps</small></span><span class="rating">${esc(r.source || '')} ${esc(r.colo || '')}</span></div>
        </div>
        <dl class="kv"><dt>Idle RTT</dt><dd class="num">${ms(r.idle_ms)}</dd><dt>Loaded RTT</dt><dd class="num">${ms(r.loaded_ms)}</dd></dl>
        <div><h3>What this means</h3><p>${['C', 'D', 'F'].includes(r.grade) ? 'Latency balloons when the connection is busy — classic bufferbloat in the modem or router queue. Enable Smart Queue Management (SQM, fq_codel or CAKE) on the router; more bandwidth will not fix this.' : 'Latency stays low while the link is saturated. Downloads, backups and other people streaming will not wreck your games or calls.'}</p></div>`;
    } else if (kind === 'stability') {
      const max = Math.max(1, ...r.samples.filter((v) => v > 0)) * 1.1;
      const strip = `<svg class="strip" viewBox="0 0 ${r.samples.length} 40" preserveAspectRatio="none" aria-label="RTT per probe">${r.samples.map((v, i) => v > 0 ? `<rect x="${i}" y="${(40 - (v / max) * 40).toFixed(1)}" width="1" height="${((v / max) * 40).toFixed(1)}" fill="var(--s-net)"/>` : `<rect x="${i}" y="0" width="1" height="40" fill="var(--bad)"/>`).join('')}</svg>`;
      body = `
        <div class="tiles">
          <div class="tile ${rateClass(r.rating)}"><span class="caption">Verdict</span><span class="value" style="font-size:22px">${esc(r.rating)}</span><span class="rating">${esc(r.verdict)}</span></div>
          <div class="tile ${r.loss_pct > 1 ? 'r-poor' : r.loss_pct > 0 ? 'r-fair' : 'r-excellent'}"><span class="caption">Loss</span><span class="value">${r.loss_pct.toFixed(1)}<small>%</small></span><span class="rating">${r.sent - r.recv} of ${r.sent} lost · gap ${r.max_gap}</span></div>
        </div>
        <div><h3>RTT per probe · ${r.name} ${esc(r.target)}</h3>${strip}<div class="muted" style="font-size:11px;margin-top:4px">${r.samples.length} probes over ${num(r.duration_s, 1)} s · red = lost</div></div>
        <dl class="kv">
          <dt>p50 / p95 / p99</dt><dd class="num">${msN(r.p50_ms)} / ${msN(r.p95_ms)} / ${msN(r.p99_ms)} ms</dd>
          <dt>Max</dt><dd class="num">${ms(r.max_ms)}</dd>
          <dt>Jitter</dt><dd class="num">${ms(r.jitter_ms)} <span class="muted">RFC 3550</span></dd>
          <dt>Spikes</dt><dd class="num">${r.spikes}</dd>
        </dl>
        ${r.detail ? `<div><h3>Detail</h3><p>${esc(r.detail)}</p></div>` : ''}`;
    } else if (kind === 'dns') {
      body = `
        <div class="diag ${(r.recommendation || '').includes('Nothing to change') || (r.recommendation || '').includes('already the fastest') ? 'clean' : 'deg'}"><span>${(r.recommendation || '').includes('witch') ? '→' : '✓'}</span><div>${esc(r.recommendation)}</div></div>
        <div class="list">${(r.resolvers || []).map((x) => `
          <div class="row ${x.name === r.current ? 'is-current' : ''}">
            <span class="pill ${x.ok ? '' : 'down'}">#${x.rank}</span>
            <div class="main"><b>${esc(x.name)}${x.name === r.current ? ' <span class="muted">(yours)</span>' : ''}</b><span>${esc(x.addr || 'system resolver')} · ${x.queries - x.failed}/${x.queries} ok · miss ${ms(x.uncached_ms)}</span></div>
            <div class="val ${rateClass(x.rating)}" style="color:var(--rating)">${x.ok ? ms(x.median_ms) : 'fail'}<small>p95 ${msN(x.p95_ms)}</small></div>
          </div>`).join('')}</div>
        <p class="muted" style="font-size:11px">Median of ${(r.resolvers[0] || {}).queries || 0} lookups of popular names per resolver; "miss" is one lookup of a random name nobody has cached.</p>`;
    }
    openSheet(TESTS[kind].title, body + `<p class="muted" style="font-size:11px">${day(r.when)} ${clock(r.when)}</p>`);
  }

  async function runTest(kind) {
    if (running.kind) { toast(`${TESTS[running.kind].title} is still running`); return; }
    running.kind = kind;
    window.renderTestsCard();
    progressSheet(kind);
    let r = null;
    try { r = await bridge.call(TESTS[kind].method); }
    finally {
      running.kind = null;
      if (r && !r.error && S.snap) { S.snap.tests = S.snap.tests || {}; S.snap.tests[TESTS[kind].key] = r; }
      window.renderTestsCard();
    }
    if (r && !r.error) toast(`${TESTS[kind].title} finished`, 'good'); else toast(`${TESTS[kind].title} failed`, 'bad');
    if (running.sheetOpen) resultSheet(kind, r);
  }

  document.addEventListener('click', (e) => {
    const run = e.target.closest('[data-run]');
    if (run) { e.stopPropagation(); runTest(run.dataset.run); return; }
    const row = e.target.closest('[data-test]');
    if (row) { const k = row.dataset.test; const r = S.snap && S.snap.tests && S.snap.tests[TESTS[k].key]; if (r) { running.sheetOpen = true; resultSheet(k, r); } else runTest(k); }
    if (e.target.closest('#sheet-close, #sheet-scrim')) running.sheetOpen = false;
    if (e.target.closest('#btn-bb')) { e.stopPropagation(); runTest('bufferbloat'); }
  }, true);
  document.addEventListener('keydown', (e) => { if (e.key === 'Escape') running.sheetOpen = false; });
  bridge.on('bufferbloat-done', () => { if (S.snap) bridge.call('Snapshot').then((s) => { if (s) { S.snap = s; window.renderTestsCard(); } }); });
})();
