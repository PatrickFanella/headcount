// Charts added on top of the stream timeline: engagement map, periodicity,
// spike synchrony, chatter account ages, channel history and sparklines.
import {
  el, n, q, pct, dec, esc, time, dateTime, compact, niceTicks, rangeTicks, tipRow, showTooltip, showTooltipAt,
  hideTooltip, svgX, svgY, reducedMotion, autocorrelation, median,
} from './kit.js';

const BANDS = [
  { min: 75, label: '75–100', cls: 'q4' },
  { min: 50, label: '50–74', cls: 'q3' },
  { min: 25, label: '25–49', cls: 'q2' },
  { min: -1, label: '0–24', cls: 'q1' },
];
const bandOf = (v) => BANDS.find((b) => v >= b.min);

function widthOf(container) {
  return Math.max(300, Math.floor(container.clientWidth || 900));
}

function makeSvg(parent, width, height, label) {
  const svg = el('svg', { viewBox: `0 0 ${width} ${height}`, height, role: 'img', 'aria-label': label });
  parent.appendChild(svg);
  return svg;
}

// Log-scale ticks at 1, 3 per decade (1-2-5 when the range is narrow).
function logTicks(lo, hi) {
  const out = [];
  const decades = Math.log10(hi / lo);
  const mults = decades > 2.2 ? [1, 3] : [1, 2, 5];
  for (let e = Math.floor(Math.log10(lo)) - 1; e <= Math.ceil(Math.log10(hi)); e++) {
    for (const m of mults) {
      const v = m * 10 ** e;
      if (v >= lo * 0.999 && v <= hi * 1.001) out.push(v);
    }
  }
  return out;
}

const fmtTick = (v) => (v < 1 ? String(+v.toFixed(2)) : compact(v));

// ---------- engagement map ----------

export function renderEngagementMap(container, data, lang) {
  const pts = data.points.filter((p) => !lang || p.language === lang)
    .filter((p) => p.median_viewers > 0 && p.chatters_per_k != null);
  const fit = data.fits[lang] || data.fits[''];
  const fitNote = lang && !data.fits[lang] && fit ? 'too few streams for a separate line; showing the all-language line' : '';
  container.innerHTML = '';
  if (!pts.length) {
    container.innerHTML = '<div class="empty">No scored streams with chat data in this window.</div>';
    return;
  }
  const legend = document.createElement('div');
  legend.className = 'legend';
  legend.innerHTML = `<span class="muted">Stream quotient</span>
    ${BANDS.slice().reverse().map((b) => `<span><svg class="key" width="12" height="12" aria-hidden="true"><circle cx="6" cy="6" r="4.5" class="pt ${b.cls}"/></svg>${b.label}</span>`).join('')}
    <span><svg class="key" width="12" height="12" aria-hidden="true"><circle cx="6" cy="6" r="4" class="pt q3 provisional"/></svg>provisional (still live)</span>
    ${fit ? `<span><i style="background:var(--text-muted)"></i>expected for audience size</span>
    <span><i class="band"></i>typical range (±1 and ±2 SD)</span>` : ''}`;
  container.appendChild(legend);
  if (fitNote) {
    const p = document.createElement('p');
    p.className = 'muted small';
    p.textContent = `Line: ${fitNote}.`;
    container.appendChild(p);
  }

  const wrap = document.createElement('div');
  wrap.className = 'chart';
  container.appendChild(wrap);
  const width = widthOf(wrap);
  const height = width < 600 ? 360 : 480;
  const m = { l: 52, r: 16, t: 12, b: 40 };
  const pw = width - m.l - m.r, ph = height - m.t - m.b;
  const yv = (p) => Math.max(0.1, p);
  const xs = pts.map((p) => p.median_viewers), ys = pts.map((p) => yv(p.chatters_per_k));
  const xlo = Math.min(...xs) / 1.25, xhi = Math.max(...xs) * 1.25;
  const ylo = Math.min(...ys) / 1.5, yhi = Math.max(...ys) * 1.5;
  const x = (v) => m.l + ((Math.log10(v) - Math.log10(xlo)) / (Math.log10(xhi) - Math.log10(xlo))) * pw;
  const y = (v) => m.t + ph - ((Math.log10(yv(v)) - Math.log10(ylo)) / (Math.log10(yhi) - Math.log10(ylo))) * ph;
  const svg = makeSvg(wrap, width, height,
    `Scatter of ${pts.length} streams: median viewers against unique chatters per 1,000 viewers, both on log scales`);
  el('clipPath', { id: 'em-clip' }, el('defs', {}, svg)).appendChild(el('rect', { x: m.l, y: m.t, width: pw, height: ph }));

  const axis = el('g', { class: 'axis' }, svg);
  for (const tv of logTicks(ylo, yhi)) {
    el('line', { class: 'gridline', x1: m.l, x2: width - m.r, y1: y(tv), y2: y(tv) }, axis);
    el('text', { x: m.l - 6, y: y(tv) + 4, 'text-anchor': 'end' }, axis).textContent = fmtTick(tv);
  }
  const xt = logTicks(xlo, xhi);
  for (const tv of xt) {
    el('line', { class: 'gridline', x1: x(tv), x2: x(tv), y1: m.t, y2: m.t + ph }, axis);
    el('text', { x: x(tv), y: height - m.b + 16, 'text-anchor': 'middle' }, axis).textContent = fmtTick(tv);
  }
  el('text', { x: m.l + pw / 2, y: height - 4, 'text-anchor': 'middle', class: 'axis-title' }, axis).textContent = 'Median concurrent viewers (log scale)';
  el('text', { x: 12, y: m.t + ph / 2, 'text-anchor': 'middle', class: 'axis-title', transform: `rotate(-90 12 ${m.t + ph / 2})` }, axis)
    .textContent = 'Unique chatters per 1,000 viewers (log)';

  const expect = fit ? (v, k = 0) => Math.exp(fit.a + fit.slope * Math.log(v) + k * fit.sd) - 0.1 : null;
  if (fit) {
    const g = el('g', { 'clip-path': 'url(#em-clip)' }, svg);
    const steps = 40;
    const at = (i) => xlo * (xhi / xlo) ** (i / steps);
    const band = (k) => {
      const up = [], dn = [];
      for (let i = 0; i <= steps; i++) {
        up.push(`${x(at(i)).toFixed(1)},${y(expect(at(i), k)).toFixed(1)}`);
        dn.push(`${x(at(i)).toFixed(1)},${y(expect(at(i), -k)).toFixed(1)}`);
      }
      return `M${up.join('L')}L${dn.reverse().join('L')}Z`;
    };
    el('path', { d: band(2), class: 'fit-band outer' }, g);
    el('path', { d: band(1), class: 'fit-band' }, g);
    const line = [];
    for (let i = 0; i <= steps; i++) line.push(`${i ? 'L' : 'M'}${x(at(i)).toFixed(1)},${y(expect(at(i))).toFixed(1)}`);
    el('path', { d: line.join(''), class: 'fit-line' }, g);
    // Plain-language note in the empty lower right, under the band.
    const ny = Math.min(m.t + ph - 10, y(expect(xhi / 1.6, -2)) + 26);
    if (ny < m.t + ph - 4 && width >= 600) {
      el('text', { x: width - m.r - 6, y: ny, 'text-anchor': 'end', class: 'annot' }, svg)
        .textContent = 'below the band: big audience, quiet chat';
    }
  }

  const layer = el('g', {}, svg);
  const order = pts.map((p, i) => i).sort((a, b) => pts[a].quotient - pts[b].quotient);
  const animate = !reducedMotion() && fit;
  const nodes = [];
  for (const i of order) {
    const p = pts[i];
    const cx = x(p.median_viewers), cy = y(p.chatters_per_k);
    const b = bandOf(p.quotient);
    const c = el('circle', { cx, cy, r: p.quotient >= 75 ? 5.5 : 4.5, class: `pt ${b.cls}${p.final ? '' : ' provisional'}` }, layer);
    nodes[i] = c;
    if (animate) {
      // Each point starts on the expected line and settles at its actual
      // value, so the distance it travels is its gap from expected.
      c.style.transform = `translateY(${(y(expect(p.median_viewers)) - cy).toFixed(1)}px)`;
      c.style.transitionDelay = `${Math.round(((cx - m.l) / pw) * 500)}ms`;
    }
  }
  if (animate) requestAnimationFrame(() => requestAnimationFrame(() => { for (const c of nodes) if (c) c.style.transform = ''; }));

  const hit = el('rect', { x: m.l, y: m.t, width: pw, height: ph, fill: 'transparent', class: 'hit' }, svg);
  let active = -1;
  const scale = () => svg.getBoundingClientRect().width / width;
  const find = (ev) => {
    const px = svgX(svg, width, ev), py = svgY(svg, height, ev);
    let best = -1, bd = (12 / scale()) ** 2 * 4; // 24px target
    for (let i = 0; i < pts.length; i++) {
      const dx = x(pts[i].median_viewers) - px, dy = y(pts[i].chatters_per_k) - py;
      const d = dx * dx + dy * dy;
      if (d < bd) { bd = d; best = i; }
    }
    return best;
  };
  hit.addEventListener('pointermove', (ev) => {
    const i = find(ev);
    if (active >= 0 && nodes[active]) nodes[active].classList.remove('hover');
    active = i;
    hit.style.cursor = i >= 0 ? 'pointer' : 'default';
    if (i < 0) { hideTooltip(); return; }
    nodes[i].classList.add('hover');
    layer.appendChild(nodes[i]);
    const p = pts[i];
    showTooltip(ev, `<div class="t"><b>${esc(p.display_name)}</b> · ${dateTime(p.started_at)}${p.game_name ? ` · ${esc(p.game_name)}` : ''}</div>
      ${tipRow('transparent', 'Median viewers', n(p.median_viewers))}
      ${tipRow('transparent', 'Chatters per 1k viewers', dec(p.chatters_per_k))}
      ${p.expected_per_k != null ? tipRow('transparent', 'Expected per 1k (scorer)', dec(p.expected_per_k)) : ''}
      ${p.z != null ? tipRow('transparent', 'Distance from expected', `${dec(p.z)} SD`) : ''}
      ${tipRow('transparent', `Quotient${p.final ? '' : ' (provisional)'}`, `${q(p.quotient)} · ${pct(p.confidence)} conf.`)}
      <div class="muted small">${esc(p.language || 'unknown language')} · click to open</div>`);
  });
  hit.addEventListener('pointerleave', () => {
    if (active >= 0 && nodes[active]) nodes[active].classList.remove('hover');
    active = -1;
    hideTooltip();
  });
  hit.addEventListener('click', (ev) => {
    const i = find(ev);
    if (i >= 0) location.hash = `#/s/${pts[i].stream_id}`;
  });

  // Table view: the streams furthest below expected.
  const low = pts.filter((p) => p.z != null).sort((a, b) => a.z - b.z).slice(0, 15);
  const det = document.createElement('details');
  det.className = 'table-view';
  det.innerHTML = `<summary>Table: the ${low.length} streams furthest below expected</summary>
    <table><thead><tr><th>Channel</th><th>Language</th><th class="r">Median viewers</th><th class="r">Chatters / 1k</th>
      <th class="r">Expected / 1k</th><th class="r">SD from expected</th><th class="r">Quotient</th></tr></thead>
    <tbody>${low.map((p) => `<tr data-href="#/s/${p.stream_id}"><td><a href="#/s/${p.stream_id}">${esc(p.display_name)}</a></td>
      <td>${esc(p.language)}</td><td class="r num">${n(p.median_viewers)}</td><td class="r num">${dec(p.chatters_per_k)}</td>
      <td class="r num">${dec(p.expected_per_k)}</td><td class="r num">${dec(p.z)}</td><td class="r num">${q(p.quotient)}</td></tr>`).join('')}</tbody></table>`;
  container.appendChild(det);
  det.querySelectorAll('tbody tr').forEach((tr) => tr.addEventListener('click', () => { location.hash = tr.dataset.href; }));
}

// ---------- periodicity ----------

// Picks the period as detect.PeriodicSpikes does, for streams scored before
// the component recorded one.
function detectPeriod(acf, maxLag) {
  const isPeak = (l) => acf[l] > acf[l - 1] && acf[l] >= acf[l + 1];
  let best = 0, bestV = 0;
  for (let l = 5; l <= maxLag; l++) if (isPeak(l) && acf[l] > bestV) { best = l; bestV = acf[l]; }
  for (let l = 5; l < best; l++) if (isPeak(l) && acf[l] >= 0.85 * bestV) { best = l; break; }
  return best;
}

export function renderPeriodicity(container, prep, comp) {
  container.innerHTML = '';
  if (!prep || prep.rel.length < 90 || prep.coverage < 0.7) {
    container.innerHTML = '<p class="muted">Needs at least 90 minutes of viewer data with few gaps; this stream has too little to test for a repeating pattern.</p>';
    return;
  }
  const rel = prep.rel;
  const len = rel.length;
  const maxLag = Math.min(120, Math.floor(len / 3));
  const acf = new Array(maxLag + 2).fill(0);
  for (let l = 1; l <= maxLag + 1; l++) acf[l] = autocorrelation(rel, l);
  const stored = comp?.available ? comp.values?.period_min : null;
  const period = stored != null ? stored : detectPeriod(acf, maxLag);
  const strength = comp?.available ? comp.strength : null;

  const grid = document.createElement('div');
  grid.className = 'pair';
  container.appendChild(grid);
  const left = document.createElement('div');
  left.className = 'chart';
  const right = document.createElement('div');
  right.className = 'chart';
  grid.append(left, right);
  left.innerHTML = `<div class="label">Autocorrelation of the detrended viewer count<small>lag in minutes</small></div>`;
  right.innerHTML = `<div class="label">${period ? `Every ${period}-minute cycle stacked` : 'Cycles stacked'}<small>% above the rolling median</small></div>`;

  // Autocorrelation columns.
  {
    const width = widthOf(left), height = 200;
    const m = { l: 40, r: 8, t: 18, b: 26 };
    const pw = width - m.l - m.r, ph = height - m.t - m.b;
    const lo = Math.min(-0.2, ...acf.slice(1, maxLag + 1)), hi = Math.max(0.4, ...acf.slice(1, maxLag + 1));
    const ticks = rangeTicks(lo, hi, 4);
    const y0 = ticks[0], y1 = ticks[ticks.length - 1];
    const x = (l) => m.l + ((l - 0.5) / maxLag) * pw;
    const y = (v) => m.t + ph - ((v - y0) / (y1 - y0)) * ph;
    const svg = makeSvg(left, width, height, `Autocorrelation by lag, 1 to ${maxLag} minutes${period ? `; detected period ${period} minutes` : ''}`);
    const axis = el('g', { class: 'axis' }, svg);
    for (const tv of ticks) {
      el('line', { class: tv === 0 ? 'zeroline' : 'gridline', x1: m.l, x2: width - m.r, y1: y(tv), y2: y(tv) }, axis);
      el('text', { x: m.l - 6, y: y(tv) + 4, 'text-anchor': 'end' }, axis).textContent = tv.toFixed(1);
    }
    const lagStep = maxLag > 60 ? 20 : 10;
    for (let l = lagStep; l <= maxLag; l += lagStep) el('text', { x: x(l), y: height - 8, 'text-anchor': 'middle' }, axis).textContent = l;
    // Values inside this band are what an unpatterned series of this length gives.
    const noise = 1.96 / Math.sqrt(len);
    el('rect', { x: m.l, y: y(noise), width: pw, height: Math.max(0, y(-noise) - y(noise)), class: 'noise-band' }, svg);
    const slot = pw / maxLag;
    const bw = Math.max(1, Math.min(8, slot - 1));
    const bars = [];
    for (let l = 1; l <= maxLag; l++) {
      const v = acf[l];
      const isP = period && l === period;
      const isMult = period && l > period && l % period === 0;
      const top = y(Math.max(0, v)), base = y(Math.min(0, v));
      bars[l] = el('rect', { x: x(l) - bw / 2, y: top, width: bw, height: Math.max(0.5, base - top), rx: Math.min(1.5, bw / 2),
        class: `acf${isP ? ' period' : isMult ? ' multiple' : ''}` }, svg);
    }
    if (period && period <= maxLag) {
      const tx = x(period);
      const label = `${period} min · r = ${acf[period].toFixed(2)}`;
      const anchor = tx > width - 110 ? 'end' : 'start';
      el('text', { x: tx + (anchor === 'start' ? 6 : -6), y: Math.max(m.t - 4, y(acf[period]) - 6), 'text-anchor': anchor, class: 'annot strong' }, svg).textContent = label;
    }
    const hit = el('rect', { x: m.l, y: 0, width: pw, height, fill: 'transparent' }, svg);
    hit.addEventListener('pointermove', (ev) => {
      const l = Math.max(1, Math.min(maxLag, Math.round(((svgX(svg, width, ev) - m.l) / pw) * maxLag + 0.5)));
      showTooltip(ev, `<div class="t">Lag ${l} min${period && l === period ? ' (detected period)' : ''}</div>
        ${tipRow('transparent', 'Autocorrelation', acf[l].toFixed(2))}
        <div class="muted small">noise level ±${noise.toFixed(2)}</div>`);
    });
    hit.addEventListener('pointerleave', hideTooltip);
  }

  // Epoch-folded overlay.
  if (!period || period < 4 || len / period < 2) {
    right.insertAdjacentHTML('beforeend', '<p class="muted">No repeating period was found, so there is nothing to stack.</p>');
  } else {
    const P = period;
    // Put the typical peak about a third of the way in.
    const phaseMed = (off) => {
      const out = [];
      for (let ph = 0; ph < P; ph++) {
        const col = [];
        for (let i = off + ph; i < len; i += P) col.push(rel[i]);
        out.push(median(col));
      }
      return out;
    };
    const med0 = phaseMed(0);
    const peak = med0.indexOf(Math.max(...med0));
    const offset = (peak - Math.round(P / 3) + P) % P;
    const cycles = [];
    for (let s = offset; s + P <= len; s += P) cycles.push(rel.slice(s, s + P + 1).map((v) => v * 100));
    const cols = [];
    for (let ph = 0; ph <= P; ph++) cols.push(cycles.map((c) => c[ph]).filter((v) => v != null));
    const med = cols.map((c) => median(c));
    const quart = (c, f) => { const s = c.slice().sort((a, b) => a - b); return s[Math.floor(f * (s.length - 1))]; };
    const all = cycles.flat().sort((a, b) => a - b);
    const lo = Math.min(-5, all[Math.floor(all.length * 0.01)]), hi = Math.max(10, all[Math.floor(all.length * 0.99)]);
    const width = widthOf(right), height = 200;
    const m = { l: 44, r: 8, t: 18, b: 26 };
    const pw = width - m.l - m.r, ph = height - m.t - m.b;
    const ticks = rangeTicks(lo, hi, 4);
    const y0 = ticks[0], y1 = ticks[ticks.length - 1];
    const x = (i) => m.l + (i / P) * pw;
    const y = (v) => m.t + ph - ((v - y0) / (y1 - y0)) * ph;
    const svg = makeSvg(right, width, height, `${cycles.length} cycles of ${P} minutes overlaid, with their median`);
    el('clipPath', { id: 'fold-clip' }, el('defs', {}, svg)).appendChild(el('rect', { x: m.l, y: m.t, width: pw, height: ph }));
    const axis = el('g', { class: 'axis' }, svg);
    for (const tv of ticks) {
      el('line', { class: tv === 0 ? 'zeroline' : 'gridline', x1: m.l, x2: width - m.r, y1: y(tv), y2: y(tv) }, axis);
      el('text', { x: m.l - 6, y: y(tv) + 4, 'text-anchor': 'end' }, axis).textContent = `${tv > 0 ? '+' : ''}${compact(tv)}%`;
    }
    const xStep = P > 40 ? 10 : 5;
    for (let i = 0; i <= P; i += xStep) el('text', { x: x(i), y: height - 8, 'text-anchor': 'middle' }, axis).textContent = `${i}m`;
    const g = el('g', { 'clip-path': 'url(#fold-clip)' }, svg);
    const pathOf = (vals) => vals.map((v, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join('');
    const animate = !reducedMotion();
    const stagger = Math.min(90, 1600 / cycles.length);
    cycles.forEach((c, k) => {
      const p = el('path', { d: pathOf(c), class: 'line cycle' }, g);
      if (animate) { p.classList.add('fade-in'); p.style.animationDelay = `${Math.round(k * stagger)}ms`; }
    });
    const mp = el('path', { d: pathOf(med), class: 'line median-line' }, g);
    if (animate) {
      mp.classList.add('draw');
      mp.style.animationDelay = `${Math.round(cycles.length * stagger)}ms`;
      requestAnimationFrame(() => mp.style.setProperty('--len', mp.getTotalLength()));
    }
    right.insertAdjacentHTML('beforeend', `<div class="legend compact"><span><i class="faint"></i>each cycle (${cycles.length})</span>
      <span><i style="background:var(--series-1)"></i>median of cycles</span></div>`);
    const cross = el('line', { class: 'crosshair', y1: m.t, y2: m.t + ph, visibility: 'hidden' }, svg);
    const hit = el('rect', { x: m.l, y: 0, width: pw, height, fill: 'transparent' }, svg);
    hit.addEventListener('pointermove', (ev) => {
      const i = Math.max(0, Math.min(P, Math.round(((svgX(svg, width, ev) - m.l) / pw) * P)));
      cross.setAttribute('x1', x(i)); cross.setAttribute('x2', x(i)); cross.setAttribute('visibility', 'visible');
      const sign = (v) => `${v > 0 ? '+' : ''}${v.toFixed(1)}%`;
      showTooltip(ev, `<div class="t">Minute ${i} of ${P}</div>
        ${tipRow('var(--series-1)', 'Median', sign(med[i]))}
        ${tipRow('transparent', 'Middle half of cycles', `${sign(quart(cols[i], 0.25))} to ${sign(quart(cols[i], 0.75))}`)}`);
    });
    hit.addEventListener('pointerleave', () => { cross.setAttribute('visibility', 'hidden'); hideTooltip(); });
  }
  const r = period && acf[period] != null ? `, autocorrelation ${acf[period].toFixed(2)}` : '';
  const summary = !period
    ? 'No repeating period stood out.'
    : strength != null && strength < 0.05
      ? `Strongest repeat: every <b>${period} minutes</b>${r}. The detector gave it ${pct(strength)} strength: too weak, too small or not spike-shaped to count.`
      : `Detected period: <b>${period} minutes</b>${r}${strength != null ? ` (component strength ${pct(strength)})` : ''}.`;
  container.insertAdjacentHTML('beforeend', `<p class="caption">${summary} A tall highlighted column well above the grey noise band means the
    viewer count rises and falls on a fixed schedule; in the stacked view, cycles that trace the same sawtooth show that rhythm directly.
    A rotating view-bot can produce this, but so can scheduled ads, timers or polls, so it is a signal, not proof. Repeated Helix values are
    treated as missing and interpolated, as the scorer does.</p>`);
}

// ---------- spike synchrony raster ----------

export function renderSynchrony(container, data, ownLabel, scored = true) {
  container.innerHTML = '';
  if (!data) {
    container.innerHTML = '<p class="muted">Spike timing could not be loaded.</p>';
    return;
  }
  if (!data.own.length) {
    container.innerHTML = scored
      ? '<p class="muted">No spike onsets were detected in this stream: its viewers never rose 8% or more above the rolling median for two minutes.</p>'
      : '<p class="muted">Spike onsets are recorded when the stream is scored; this stream has not been scored yet.</p>';
    return;
  }
  const t0 = new Date(data.from).getTime() / 1000, t1 = new Date(data.to).getTime() / 1000;
  const tol = data.tolerance_min * 60;
  const ownMin = data.own.map((o) => Math.floor(o.ts / 60));
  const matched = (t) => { const mm = Math.floor(t / 60); return ownMin.some((o) => Math.abs(o - mm) <= data.tolerance_min); };
  const rows = [{ label: ownLabel, own: true, onsets: data.own.map((o) => o.ts), rel: data.own.map((o) => o.rel) },
    ...data.partners.map((p) => ({ label: p.display_name, login: p.login, onsets: p.onsets, p }))];
  const wrap = document.createElement('div');
  wrap.className = 'chart';
  container.appendChild(wrap);
  const width = widthOf(wrap);
  const narrow = width < 640;
  const rowH = 28;
  const m = { l: narrow ? 96 : 150, r: narrow ? 70 : 132, t: 6, b: 24 };
  const height = m.t + rows.length * rowH + m.b;
  const pw = width - m.l - m.r;
  const x = (t) => m.l + ((t - t0) / Math.max(60, t1 - t0)) * pw;
  const svg = makeSvg(wrap, width, height, `Spike onset times for this stream and ${data.partners.length} other channels`);
  const axis = el('g', { class: 'axis' }, svg);
  const hour = 3600;
  const every = Math.max(1, Math.round((t1 - t0) / hour / (narrow ? 4 : 8)));
  for (let tt = Math.ceil(t0 / hour) * hour; tt <= t1; tt += hour * every) {
    el('line', { class: 'gridline', x1: x(tt), x2: x(tt), y1: m.t, y2: height - m.b }, axis);
    el('text', { x: x(tt), y: height - 6, 'text-anchor': 'middle' }, axis).textContent = time(tt * 1000);
  }
  for (const o of data.own) {
    el('rect', { x: x(o.ts - tol), y: m.t, width: Math.max(2, x(o.ts + tol) - x(o.ts - tol)), height: rows.length * rowH, class: 'onset-window' }, svg);
  }
  const clip = (s, max) => (s.length > max ? `${s.slice(0, max - 1)}…` : s);
  rows.forEach((r, i) => {
    const cy = m.t + i * rowH + rowH / 2;
    el('line', { class: 'lane', x1: m.l, x2: width - m.r, y1: cy, y2: cy }, svg);
    const label = el('text', { x: m.l - 8, y: cy + 4, 'text-anchor': 'end', class: `lane-label${r.own ? ' strong' : ''}` }, svg);
    label.textContent = clip(r.label, narrow ? 12 : 20);
    if (r.login) {
      const a = el('a', { href: `#/c/${encodeURIComponent(r.login)}` }, svg);
      a.appendChild(label);
    }
    const right = el('text', { x: width - m.r + 8, y: cy + 4, class: 'lane-note' }, svg);
    right.textContent = r.own ? `${r.onsets.length} onsets` : narrow ? `${r.p.matches} matched` : `${r.p.matches} matched · ${dec(r.p.expected)} by chance`;
    r.onsets.forEach((t, j) => {
      const hitM = r.own || matched(t);
      el('rect', { x: x(t) - 1.5, y: cy - 8, width: 3, height: 16, rx: 1.5, class: `tick${hitM ? ' matched' : ''}` }, svg);
      const h = el('rect', { x: x(t) - 6, y: cy - rowH / 2, width: 12, height: rowH, fill: 'transparent', tabindex: 0, class: 'tick-hit' }, svg);
      const html = `<div class="t"><b>${esc(r.label)}</b> · ${time(t * 1000)}</div>
        ${r.own ? tipRow('transparent', 'Rise above baseline', `+${Math.round(r.rel[j] * 100)}%`)
          : tipRow('transparent', `Within ±${data.tolerance_min} min of this stream’s onset`, hitM ? 'yes' : 'no')}`;
      h.addEventListener('pointerenter', (ev) => showTooltip(ev, html));
      h.addEventListener('pointerleave', hideTooltip);
      h.addEventListener('focus', () => showTooltipAt(h, html));
      h.addEventListener('blur', hideTooltip);
    });
  });
  const named = data.partners.find((p) => p.named);
  container.insertAdjacentHTML('beforeend', `<div class="legend compact">
      <span><i style="background:var(--series-1)"></i>onset within ±${data.tolerance_min} min of this stream’s</span>
      <span><i style="background:var(--baseline)"></i>other onset</span>
      <span><i class="band"></i>this stream’s onset ±${data.tolerance_min} min</span></div>
    <p class="caption">Each tick is a minute when a channel’s viewers began rising 8% or more above its rolling median. Listed channels
    (${data.partners.length} of ${n(data.partners_tested)} with enough overlap) are those whose onsets fall in this stream’s shaded windows most
    often compared with chance${named ? `; the scorer’s strongest match was <b>${esc(named.display_name)}</b>` : ''}. A view-bot service that
    moves bots between customers can make channels spike together, but so can a shared event or a Twitch-wide count change, so a match
    is a signal, not proof.</p>`);
}

// ---------- chatter account ages ----------

export function renderAccountAges(container, data, withSummary = true) {
  container.innerHTML = '';
  if (!data || !data.chatters) {
    container.innerHTML = '<p class="muted">No chatter list is stored for this stream (chat was not collected, or the list has been pruned for an old stream).</p>';
    return;
  }
  const notLooked = data.chatters - data.known;
  const dated = data.months.reduce((a, b) => a + b[1], 0);
  if (withSummary) container.insertAdjacentHTML('beforeend', `<p class="stats-line"><b class="num">${n(data.chatters)}</b> chatters ·
    <b class="num">${n(dated)}</b> with a known creation date · <b class="num">${n(data.missing)}</b> suspended or deleted
    ${notLooked ? `· <b class="num">${n(notLooked)}</b> not looked up yet` : ''}</p>`);
  if (!dated) {
    container.insertAdjacentHTML('beforeend', '<p class="muted">No creation dates looked up yet.</p>');
    return;
  }
  const start = new Date(data.started_at);
  // Weeks: the 52 weeks before the stream, zero-filled.
  const weekMap = new Map(data.weeks);
  const monday = (d) => { const c = new Date(Date.UTC(d.getUTCFullYear(), d.getUTCMonth(), d.getUTCDate())); c.setUTCDate(c.getUTCDate() - ((c.getUTCDay() + 6) % 7)); return c; };
  const weeks = [];
  const lastWeek = data.weeks.length ? new Date(`${data.weeks[data.weeks.length - 1][0]}T00:00:00Z`) : start;
  const weekEnd = lastWeek > start ? lastWeek : start;
  for (let w = monday(new Date(start.getTime() - 364 * 864e5)); w <= weekEnd; w = new Date(w.getTime() + 7 * 864e5)) {
    const k = w.toISOString().slice(0, 10);
    weeks.push({ key: k, date: w, v: weekMap.get(k) || 0 });
  }
  const monthMap = new Map(data.months);
  const first = data.months[0][0];
  const months = [];
  let [yy, mm] = first.split('-').map(Number);
  const startKey = `${start.getUTCFullYear()}-${String(start.getUTCMonth() + 1).padStart(2, '0')}`;
  const lastKey = data.months[data.months.length - 1][0];
  const endKey = lastKey > startKey ? lastKey : startKey;
  for (let guard = 0; guard < 400; guard++) {
    const k = `${yy}-${String(mm).padStart(2, '0')}`;
    months.push({ key: k, date: new Date(Date.UTC(yy, mm - 1, 1)), v: monthMap.get(k) || 0 });
    if (k >= endKey) break;
    mm++; if (mm > 12) { mm = 1; yy++; }
  }
  const cutoff = start.getTime() - 30 * 864e5;
  columnChart(container, {
    label: weekEnd > start ? 'Accounts created per week, from a year before the stream' : 'Accounts created per week, year before the stream',
    note: 'shaded: the 30 days the scorer counts as new',
    items: weeks, height: 150, highlightFrom: cutoff, end: start.getTime() + 7 * 864e5,
    tick: (it, i) => (it.date.getUTCDate() <= 7 && i > 0 ? it.date.toLocaleDateString([], { month: 'short', timeZone: 'UTC' }) : null),
    tip: (it) => `Week of ${it.date.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric', timeZone: 'UTC' })}`,
  });
  const yearStep = months.length > 120 ? 2 : 1;
  columnChart(container, {
    label: 'Accounts created per month, all time', items: months, height: 120,
    tick: (it) => (it.date.getUTCMonth() === 0 && it.date.getUTCFullYear() % yearStep === 0 ? String(it.date.getUTCFullYear()) : null),
    tip: (it) => it.date.toLocaleDateString([], { month: 'long', year: 'numeric', timeZone: 'UTC' }),
  });
  container.insertAdjacentHTML('beforeend', `<p class="caption">Organic chat has accounts of all ages, thinning towards the present. A sharp spike of
    accounts created in the same few weeks, especially just before the stream, is what a batch of freshly made bot accounts looks like.
    It is a signal, not proof: giveaways, a viral moment or a new audience can also bring many new accounts.</p>`);
}

function columnChart(container, o) {
  const wrap = document.createElement('div');
  wrap.className = 'chart';
  wrap.innerHTML = `<div class="label">${o.label}${o.note ? `<small>${o.note}</small>` : ''}</div>`;
  container.appendChild(wrap);
  const width = widthOf(wrap), height = o.height;
  const m = { l: 44, r: 8, t: 6, b: 22 };
  const pw = width - m.l - m.r, ph = height - m.t - m.b;
  const ticks = niceTicks(Math.max(1, ...o.items.map((it) => it.v)), 3, true);
  const top = ticks[ticks.length - 1];
  const slot = pw / o.items.length;
  const gap = slot > 6 ? 2 : slot > 3 ? 1 : 0;
  const bw = Math.min(24, slot - gap);
  const x = (i) => m.l + i * slot + (slot - bw) / 2;
  const y = (v) => m.t + ph - (v / top) * ph;
  const total = o.items.reduce((a, b) => a + b.v, 0);
  const svg = makeSvg(wrap, width, height, `${o.label}: ${o.items.length} columns, ${n(total)} accounts`);
  const axis = el('g', { class: 'axis' }, svg);
  for (const tv of ticks) {
    el('line', { class: 'gridline', x1: m.l, x2: width - m.r, y1: y(tv), y2: y(tv) }, axis);
    el('text', { x: m.l - 6, y: y(tv) + 4, 'text-anchor': 'end' }, axis).textContent = compact(tv);
  }
  if (o.highlightFrom) {
    const i0 = o.items.findIndex((it) => it.date.getTime() + 7 * 864e5 > o.highlightFrom);
    if (i0 >= 0) el('rect', { x: m.l + i0 * slot, y: m.t, width: pw - i0 * slot, height: ph, class: 'onset-window' }, svg);
  }
  let lastLabelX = -1e9;
  o.items.forEach((it, i) => {
    const lbl = o.tick(it, i);
    const cx = m.l + i * slot + slot / 2;
    if (lbl && cx - lastLabelX > 34) {
      el('text', { x: cx, y: height - 6, 'text-anchor': 'middle' }, axis).textContent = lbl;
      lastLabelX = cx;
    }
    if (it.v > 0) {
      const h = Math.max(1, (it.v / top) * ph);
      const r = Math.min(bw / 2, 4, h);
      // Rounded data end, square at the baseline.
      const x0 = x(i), y0 = m.t + ph - h, x1 = x0 + bw, yb = m.t + ph;
      el('path', { class: 'col', d: `M${x0},${yb}V${y0 + r}Q${x0},${y0} ${x0 + r},${y0}H${x1 - r}Q${x1},${y0} ${x1},${y0 + r}V${yb}Z` }, svg);
    }
  });
  const hit = el('rect', { x: m.l, y: 0, width: pw, height, fill: 'transparent' }, svg);
  const hl = el('rect', { y: m.t, height: ph, width: slot, class: 'col-hover', visibility: 'hidden' }, svg);
  svg.insertBefore(hl, svg.firstChild.nextSibling);
  hit.addEventListener('pointermove', (ev) => {
    const i = Math.max(0, Math.min(o.items.length - 1, Math.floor((svgX(svg, width, ev) - m.l) / slot)));
    hl.setAttribute('x', m.l + i * slot); hl.setAttribute('visibility', 'visible');
    const it = o.items[i];
    showTooltip(ev, `<div class="t">${o.tip(it)}</div>${tipRow('var(--series-1)', 'Accounts created', n(it.v))}
      <div class="muted small">${pct(it.v / total)} of dated chatters</div>`);
  });
  hit.addEventListener('pointerleave', () => { hl.setAttribute('visibility', 'hidden'); hideTooltip(); });
}

// ---------- channel history ----------

export function renderChannelHistory(container, streams) {
  container.innerHTML = '';
  const items = streams.slice().sort((a, b) => new Date(a.started_at) - new Date(b.started_at));
  if (!items.length) return;
  const wrap = document.createElement('div');
  wrap.className = 'chart';
  container.appendChild(wrap);
  const width = widthOf(wrap);
  const rows = [
    { key: 'quotient', label: 'Quotient', note: 'faded bars had low confidence', h: 110, max: 100, cls: 'col q-col', fmt: q },
    { key: 'avg_viewers', label: 'Average viewers', h: 90, cls: 'col', fmt: compact },
    { key: 'follows_gained', label: 'Follows gained during the stream', h: 90, cls: 'col follows', fmt: compact },
  ];
  const m = { l: 48, r: 8, labelH: 30, gap: 14, b: 22 };
  const height = rows.reduce((a, r) => a + m.labelH + r.h + m.gap, 0) + m.b;
  const pw = width - m.l - m.r;
  const slot = pw / Math.max(items.length, 6);
  const bw = Math.max(3, Math.min(24, slot - 4));
  const x = (i) => m.l + i * slot + slot / 2;
  const svg = makeSvg(wrap, width, height, `Per-stream quotient, average viewers and follows gained for ${items.length} streams`);
  let top = 0;
  for (const r of rows) {
    const vals = items.map((s) => s[r.key]).filter((v) => v != null);
    const lo = Math.min(0, ...vals);
    const ticks = r.max ? [0, 50, 100] : lo < 0 ? rangeTicks(lo, Math.max(1, ...vals), 3) : niceTicks(Math.max(1, ...vals), 2);
    const y0 = ticks[0], y1 = ticks[ticks.length - 1];
    const py = top + m.labelH;
    const y = (v) => py + r.h - ((v - y0) / (y1 - y0 || 1)) * r.h;
    const t = el('text', { x: m.l, y: top + 13, class: 'row-label' }, svg);
    t.textContent = r.label;
    if (r.note) el('tspan', { class: 'row-note', dx: 8 }, t).textContent = r.note;
    const axis = el('g', { class: 'axis' }, svg);
    for (const tv of ticks) {
      el('line', { class: tv === 0 ? 'zeroline' : 'gridline', x1: m.l, x2: width - m.r, y1: y(tv), y2: y(tv) }, axis);
      el('text', { x: m.l - 6, y: y(tv) + 4, 'text-anchor': 'end' }, axis).textContent = compact(tv);
    }
    items.forEach((s, i) => {
      const v = s[r.key];
      if (v == null) {
        el('text', { x: x(i), y: y(0) - 4, 'text-anchor': 'middle', class: 'na-mark' }, svg).textContent = '–';
        return;
      }
      const a = y(Math.max(0, v)), b = y(Math.min(0, v));
      const h = Math.max(1, b - a);
      const rr = Math.min(4, bw / 2, h);
      const x0 = x(i) - bw / 2, x1 = x0 + bw;
      const d = v >= 0
        ? `M${x0},${b}V${a + rr}Q${x0},${a} ${x0 + rr},${a}H${x1 - rr}Q${x1},${a} ${x1},${a + rr}V${b}Z`
        : `M${x0},${a}V${b - rr}Q${x0},${b} ${x0 + rr},${b}H${x1 - rr}Q${x1},${b} ${x1},${b - rr}V${a}Z`;
      const p = el('path', { d, class: r.cls }, svg);
      if (r.key === 'quotient') p.style.opacity = Math.max(0.25, s.confidence ?? 0.25);
    });
    top = py + r.h + m.gap;
  }
  let lastX = -1e9;
  items.forEach((s, i) => {
    if (x(i) - lastX < 70) return;
    lastX = x(i);
    el('text', { x: x(i), y: height - 6, 'text-anchor': 'middle', class: 'axis-label' }, svg)
      .textContent = new Date(s.started_at).toLocaleDateString([], { month: 'short', day: 'numeric' });
  });
  const hl = el('rect', { y: 0, height: height - m.b, width: slot, class: 'col-hover', visibility: 'hidden' }, svg);
  svg.insertBefore(hl, svg.firstChild);
  items.forEach((s, i) => {
    const h = el('rect', { x: m.l + i * slot, y: 0, width: slot, height: height - m.b, fill: 'transparent', tabindex: 0, class: 'tick-hit', role: 'link',
      'aria-label': `Stream ${dateTime(s.started_at)}: quotient ${q(s.quotient)}, average viewers ${n(s.avg_viewers)}, follows gained ${n(s.follows_gained)}` }, svg);
    const perK = s.follows_gained != null && s.avg_viewers ? (s.follows_gained / s.avg_viewers) * 1000 : null;
    const html = `<div class="t">${dateTime(s.started_at)}${s.game_name ? ` · ${esc(s.game_name)}` : ''}</div>
      ${tipRow('var(--meter-fill)', 'Quotient', s.quotient != null ? `${q(s.quotient)} · ${pct(s.confidence)} conf.` : 'not scored')}
      ${tipRow('var(--series-1)', 'Average viewers', n(s.avg_viewers))}
      ${tipRow('var(--series-3)', 'Follows gained', n(s.follows_gained))}
      ${perK != null ? tipRow('transparent', 'Follows per 1k viewers', dec(perK)) : ''}`;
    const on = (ev) => { hl.setAttribute('x', m.l + i * slot); hl.setAttribute('visibility', 'visible'); ev ? showTooltip(ev, html) : showTooltipAt(h, html); };
    const off = () => { hl.setAttribute('visibility', 'hidden'); hideTooltip(); };
    h.addEventListener('pointermove', on);
    h.addEventListener('pointerleave', off);
    h.addEventListener('focus', () => on());
    h.addEventListener('blur', off);
    h.addEventListener('click', () => { location.hash = `#/s/${s.id}`; });
    h.addEventListener('keydown', (e) => { if (e.key === 'Enter') location.hash = `#/s/${s.id}`; });
    h.style.cursor = 'pointer';
  });
  container.insertAdjacentHTML('beforeend', `<p class="caption">Each column is one stream, oldest on the left; hover for values, click to open.
    Compare follows with audience size: a large audience that gains almost no follows, or a burst of follows with a small audience,
    is a pattern bots produce. Each is a signal, not proof.</p>`);
}

// ---------- sparkline ----------

export function sparkline(points, t0, t1) {
  if (!points || points.length < 2) return '<span class="muted small">–</span>';
  const w = 96, h = 24, pad = 3;
  const vals = points.map((p) => p[1]);
  const lo = Math.min(...vals), hi = Math.max(...vals);
  const x = (t) => pad + ((t - t0) / (t1 - t0)) * (w - 2 * pad);
  const y = (v) => h - pad - ((v - lo) / (hi - lo || 1)) * (h - 2 * pad);
  const d = points.map((p, i) => `${i ? 'L' : 'M'}${x(p[0]).toFixed(1)},${y(p[1]).toFixed(1)}`).join('');
  const last = points[points.length - 1];
  return `<svg class="spark" viewBox="0 0 ${w} ${h}" width="${w}" height="${h}" role="img"
    aria-label="Viewers over the last 3 hours, from ${n(vals[0])} to ${n(last[1])}, range ${n(lo)} to ${n(hi)}">
    <path d="${d}" class="spark-line"/><circle cx="${x(last[0]).toFixed(1)}" cy="${y(last[1]).toFixed(1)}" r="2.5" class="spark-dot"/></svg>`;
}
