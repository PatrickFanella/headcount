import {
  api, n, q, pct, time, dateTime, esc, dur, el, niceTicks, compact, nearest, tipRow, showTooltip, hideTooltip, svgX,
  reducedMotion, prepareMinutes, dec,
} from './kit.js';
import { renderEngagementMap, renderPeriodicity, renderSynchrony, renderAccountAges, renderChannelHistory, sparkline } from './viz.js';

const app = document.getElementById('app');

const COMPONENTS = {
  periodic_spikes: ['Periodic spikes', 'Viewer spikes that repeat at a fixed interval and return to the same baseline'],
  synchronized_spikes: ['Synchronized spikes', 'Viewer spikes that start in the same minutes as another channel’s, beyond chance'],
  spike_chat_lift: ['Chat during spikes', 'Whether chat grows when viewers spike; bot viewers do not chat'],
  chat_engagement: ['Chat engagement', 'Chatters per 1,000 viewers compared with streams of the same size'],
  follower_purge: ['Follower purges', 'Large follower drops, as when Twitch removes fake follows'],
  chatter_accounts: ['Chatter accounts', 'Share of chatters with accounts under 30 days old, or suspended/deleted'],
  follow_bursts: ['Follow bursts', 'Minutes with far more follows than the stream’s normal rate'],
  unexplained_jumps: ['Unexplained jumps', 'Sharp viewer changes not explained by a raid or game change'],
  spike_follow_lift: ['Follows during spikes', 'Whether follows speed up when viewers spike'],
  chat_repetition: ['Chat repetition', 'Share of chat that is one repeated message'],
  start_ramp: ['Start ramp', 'How fast viewers arrive after going live'],
};
const WEIGHTS = {
  periodic_spikes: 0.6, synchronized_spikes: 0.5, spike_chat_lift: 0.5, chat_engagement: 0.5, follower_purge: 0.4, chatter_accounts: 0.4,
  follow_bursts: 0.3, unexplained_jumps: 0.3, spike_follow_lift: 0.25, chat_repetition: 0.15, start_ramp: 0.1,
};

function meter(value, confidence) {
  if (value == null) return '<span class="muted">not scored</span>';
  if (confidence != null && confidence < 0.1) return '<span class="muted">too little data</span>';
  const low = confidence != null && confidence < 0.4;
  return `<span class="meter${low ? ' low-conf' : ''}" title="confidence ${pct(confidence)}">
    <span class="track"><span class="fill" style="display:block;width:${Math.max(2, value)}%"></span></span>
    <span class="num">${q(value)}</span></span>`;
}

function setActiveNav() {
  const h = (location.hash || '#/').split('?')[0];
  document.querySelectorAll('.top nav a').forEach((a) => a.classList.toggle('active', a.getAttribute('href') === h));
}

async function route() {
  setActiveNav();
  hideTooltip();
  const h = location.hash.replace(/^#/, '').split('?')[0] || '/';
  const parts = h.split('/').filter(Boolean);
  app.innerHTML = '<div class="empty">Loading…</div>';
  try {
    if (parts[0] === 'c' && parts[1]) await channelView(decodeURIComponent(parts[1]));
    else if (parts[0] === 's' && parts[1]) await streamView(parts[1]);
    else if (parts[0] === 'about') aboutView();
    else if (parts[0] === 'engagement') await engagementView();
    else await leaderboardView(parts[0] === 'live');
  } catch (e) {
    app.innerHTML = `<div class="empty">Couldn’t load this page: ${esc(e.message)}</div>`;
  }
}
window.addEventListener('hashchange', route);

// ---------- leaderboard ----------

async function leaderboardView(liveOnly) {
  const [ov, rows, sparks] = await Promise.all([api('/overview'), api(`/channels?limit=500${liveOnly ? '&live=1' : ''}`),
    api('/sparklines').catch(() => null)]);
  const sparkEnd = sparks ? sparks.now : 0, sparkStart = sparkEnd - (sparks?.window_minutes ?? 180) * 60;
  const hours = ov.first_sample ? (new Date(ov.last_sample) - new Date(ov.first_sample)) / 3.6e6 : 0;
  app.innerHTML = `
    <h1>${liveOnly ? 'Live now' : 'Leaderboard'}</h1>
    <p class="sub">Channels that have reached the Twitch top 200, ordered by sus quotient. A high quotient means the
      data has patterns that bots produce; it is not proof of botting.</p>
    <div class="tiles">
      <div class="tile"><div class="v num">${n(ov.channels)}</div><div class="l">channels tracked</div></div>
      <div class="tile"><div class="v num">${n(ov.live)}</div><div class="l">live now</div></div>
      <div class="tile"><div class="v num">${n(ov.scored_streams)}</div><div class="l">streams scored</div></div>
      <div class="tile"><div class="v num">${n(ov.chat_messages_24h)}</div><div class="l">chat messages, last 24h</div></div>
      <div class="tile"><div class="v num">${hours < 48 ? `${hours.toFixed(1)}h` : `${(hours / 24).toFixed(1)}d`}</div><div class="l">of data collected</div></div>
    </div>
    ${hours < 24 ? '<p class="note">Collection started recently. Scores need several full streams and a chat baseline built from many channels, so early values are provisional and change often.</p>' : ''}
    ${sparks ? '<p class="muted small table-note">“Last 3h” shows each channel’s viewers over the past three hours, scaled to its own range; a line that stops early means the channel went offline.</p>' : ''}
    <div class="table-wrap"><table>
      <thead><tr>
        <th>#</th><th>Channel</th><th class="r">Viewers</th>${sparks ? '<th title="Viewers over the last 3 hours, 10-minute averages">Last 3h</th>' : ''}<th>Category</th><th>Title</th>
        <th>Quotient</th><th class="r">Streams</th>
      </tr></thead>
      <tbody>${rows.map((c, i) => {
        const value = c.live_quotient ?? c.quotient;
        const conf = c.live_quotient != null ? c.live_confidence : c.quotient_confidence;
        return `<tr data-href="#/c/${encodeURIComponent(c.login)}">
          <td class="num muted">${i + 1}</td>
          <td>${c.live_stream_id ? '<span class="live-dot" title="live"></span>' : ''}<a href="#/c/${encodeURIComponent(c.login)}">${esc(c.display_name)}</a>
            <span class="tier">${esc(c.tier)}</span></td>
          <td class="r num">${c.viewers != null ? n(c.viewers) : '<span class="muted">offline</span>'}</td>
          ${sparks ? `<td class="spark-cell">${sparkline(sparks.channels[c.id], sparkStart, sparkEnd)}</td>` : ''}
          <td class="cat" title="${esc(c.game_name ?? '')}">${esc(c.game_name ?? '')}</td>
          <td class="title" title="${esc(c.title ?? '')}">${esc(c.title ?? '')}</td>
          <td>${meter(value, conf)}</td>
          <td class="r num">${n(c.scored_streams)}</td>
        </tr>`;
      }).join('')}</tbody>
    </table></div>`;
  app.querySelectorAll('tbody tr').forEach((tr) => tr.addEventListener('click', (e) => {
    if (e.target.closest('a')) return;
    location.hash = tr.dataset.href;
  }));
}

// ---------- channel ----------

async function channelView(login) {
  const { channel: c, streams } = await api(`/channels/${encodeURIComponent(login)}`);
  app.innerHTML = `
    <h1>${esc(c.display_name)}</h1>
    <p class="sub">Tracked since ${dateTime(c.first_seen_at)} · best rank #${c.best_rank ?? '–'} · tier ${esc(c.tier)}
      · <a href="https://twitch.tv/${encodeURIComponent(c.login)}" target="_blank" rel="noopener">twitch.tv/${esc(c.login)}</a></p>
    <div class="tiles">
      <div class="tile"><div class="v num">${q(c.quotient)}</div><div class="l">channel quotient</div></div>
      <div class="tile"><div class="v num">${pct(c.quotient_confidence)}</div><div class="l">confidence</div></div>
      <div class="tile"><div class="v num">${streams.length}</div><div class="l">streams recorded</div></div>
    </div>
    ${streams.length ? '<h2>Stream history</h2><div class="card" id="history"></div>' : ''}
    <h2>Streams</h2>
    ${streams.length ? `<table><thead><tr><th>Started</th><th>Length</th><th>Category</th><th class="r">Avg viewers</th>
      <th class="r">Peak</th><th>Quotient</th></tr></thead><tbody>
      ${streams.map((s) => `<tr data-href="#/s/${s.id}">
        <td>${s.ended_at ? '' : '<span class="live-dot" title="live"></span>'}${dateTime(s.started_at)}</td>
        <td class="num">${dur(s.started_at, s.ended_at || Date.now())}</td>
        <td>${esc(s.game_name ?? '')}</td>
        <td class="r num">${n(s.avg_viewers)}</td>
        <td class="r num">${n(s.peak_viewers)}</td>
        <td>${meter(s.quotient, s.confidence)}${s.final === false ? ' <span class="muted">provisional</span>' : ''}</td>
      </tr>`).join('')}</tbody></table>` : '<div class="empty">No streams recorded yet.</div>'}`;
  app.querySelectorAll('tbody tr').forEach((tr) => tr.addEventListener('click', () => { location.hash = tr.dataset.href; }));
  if (streams.length) renderChannelHistory(document.getElementById('history'), streams);
}

// ---------- engagement map ----------

async function engagementView() {
  const params = new URLSearchParams(location.hash.split('?')[1] || '');
  const days = ['1', '7', '14'].includes(params.get('days')) ? params.get('days') : '7';
  const data = await api(`/engagement?days=${days}`);
  const langs = [...new Set(data.points.map((p) => p.language).filter(Boolean))]
    .map((l) => [l, data.points.filter((p) => p.language === l).length]).sort((a, b) => b[1] - a[1]);
  const names = new Intl.DisplayNames([], { type: 'language' });
  const langName = (l) => { try { return names.of(l); } catch { return l; } };
  app.innerHTML = `
    <h1>Engagement map</h1>
    <p class="sub">Every recently scored stream, placed by audience size and how many of those viewers chat. Bigger streams
      have proportionally quieter chat, so the grey line shows what is typical for each size. Streams far below the band have
      a big audience but a quiet chat, which is what bot viewers produce. That is a signal, not proof: chat can also be quiet
      because of restricted chat, a non-chatty audience, or embedded and muted players.</p>
    <div class="filters" role="group" aria-label="Filters">
      <label>Window <select id="em-days">
        ${[['1', 'Last 24 hours'], ['7', 'Last 7 days'], ['14', 'Last 14 days']].map(([v, t]) => `<option value="${v}"${v === days ? ' selected' : ''}>${t}</option>`).join('')}
      </select></label>
      <label>Language <select id="em-lang"><option value="">All languages (${data.points.length})</option>
        ${langs.map(([l, c]) => `<option value="${esc(l)}">${esc(langName(l))} (${c})</option>`).join('')}
      </select></label>
      ${data.truncated ? `<span class="muted small">showing the latest ${n(data.limit)} streams</span>` : ''}
    </div>
    <div class="card" id="em"></div>
    <p class="caption">The line is refitted here from the streams shown, the same way the scorer fits its baseline (log-log, outliers
      removed); the scorer uses a per-language line when a language has enough streams. Hover a point for the scorer’s own expected
      value and its distance from it in standard deviations (SD); click to open the stream.</p>`;
  const box = document.getElementById('em');
  const langSel = document.getElementById('em-lang');
  const draw = () => renderEngagementMap(box, data, langSel.value);
  draw();
  langSel.addEventListener('change', draw);
  document.getElementById('em-days').addEventListener('change', (e) => { location.hash = `#/engagement?days=${e.target.value}`; });
}

// ---------- stream ----------

// Mirrors the Go detectors: drop repeated Helix values, interpolate, then a
// 31-minute rolling median as the baseline.
function baselineOf(points) {
  const v = points.map((p) => p[1]);
  const held = v.map((x, i) => (i > 0 && x === v[i - 1] ? null : x));
  const interp = held.slice();
  let prev = -1;
  for (let i = 0; i < held.length; i++) {
    if (held[i] == null) continue;
    if (prev >= 0 && i - prev > 1) for (let j = prev + 1; j < i; j++) interp[j] = held[prev] + ((j - prev) / (i - prev)) * (held[i] - held[prev]);
    prev = i;
  }
  for (let j = prev + 1; j < interp.length; j++) interp[j] = held[prev];
  const out = [];
  for (let i = 0; i < interp.length; i++) {
    const w = interp.slice(Math.max(0, i - 15), i + 16).filter((x) => x != null).sort((a, b) => a - b);
    out.push([points[i][0], w.length ? w[Math.floor(w.length / 2)] : null]);
  }
  return { smooth: points.map((p, i) => [p[0], interp[i]]), baseline: out };
}

function followDeltas(totals) {
  const out = [];
  for (let i = 1; i < totals.length; i++) {
    const dt = (totals[i][0] - totals[i - 1][0]) / 60;
    if (dt <= 0) continue;
    out.push([totals[i][0], (totals[i][1] - totals[i - 1][1]) / dt]);
  }
  return out;
}

async function streamView(id) {
  const [d, onsets, ages] = await Promise.all([
    api(`/streams/${id}`),
    api(`/streams/${id}/onsets`).catch(() => null),
    api(`/streams/${id}/account-ages`).catch(() => null),
  ]);
  const s = d.stream;
  const score = d.score;
  const { smooth, baseline } = baselineOf(d.viewers);
  const avg = d.viewers.length ? d.viewers.reduce((a, p) => a + p[1], 0) / d.viewers.length : null;
  const follows = followDeltas(d.followers);
  const gained = d.followers.length > 1 ? d.followers[d.followers.length - 1][1] - d.followers[0][1] : null;
  const chatters = d.chat.map((c) => [c[0], c[2]]);
  const acc = d.accounts || {};

  app.innerHTML = `
    <p class="sub" style="margin-bottom:6px"><a href="#/c/${encodeURIComponent(s.login)}">← ${esc(s.display_name)}</a></p>
    <h1>${esc(s.title || 'Untitled stream')}</h1>
    <p class="sub">${esc(s.game_name ?? '')} · ${dateTime(s.started_at)} – ${s.ended_at ? time(s.ended_at) : 'live'}
      ${s.first_seen_at && new Date(s.first_seen_at) - new Date(s.started_at) > 300000 ? `· recording began ${time(s.first_seen_at)}` : ''}</p>
    <div class="tiles">
      <div class="tile"><div class="v num">${score ? q(score.quotient) : '–'}</div><div class="l">sus quotient${score && !score.final ? ' (provisional)' : ''}</div></div>
      <div class="tile"><div class="v num">${score ? pct(score.confidence) : '–'}</div><div class="l">confidence</div></div>
      <div class="tile"><div class="v num">${n(avg)}</div><div class="l">avg viewers</div></div>
      <div class="tile"><div class="v num">${n(s.peak_viewers)}</div><div class="l">peak viewers</div></div>
      <div class="tile"><div class="v num">${gained != null ? n(gained) : '–'}</div><div class="l">followers gained</div></div>
      <div class="tile"><div class="v num">${n(acc.chatters)}</div><div class="l">unique chatters</div></div>
    </div>
    <div class="card" id="charts"></div>
    <h2>Repeating spikes</h2>
    <div class="card" id="periodicity"></div>
    <h2>Spikes at the same time as other channels</h2>
    <div class="card" id="synchrony"></div>
    <h2>Score breakdown</h2>
    <div class="card" id="components"></div>
    <h2>Chatter accounts</h2>
    <div class="card">${acc.known ? `<p class="stats-line">${n(acc.known)} of ${n(acc.chatters)} chatters looked up ·
      <b class="num">${pct(acc.new_30d / acc.known)}</b> created in the 30 days before the stream ·
      <b class="num">${pct(acc.missing / acc.known)}</b> suspended or deleted</p>` : ''}<div id="ages"></div></div>`;

  renderCharts(document.getElementById('charts'), {
    viewers: d.viewers, smooth, baseline, chatters, follows,
    events: d.events.filter((e) => e.kind === 'raid_in' || e.kind === 'game_change'),
    onsets: onsets ? onsets.own : [],
  });
  renderPeriodicity(document.getElementById('periodicity'), prepareMinutes(d.viewers), score?.components?.periodic_spikes);
  renderSynchrony(document.getElementById('synchrony'), onsets, s.display_name, !!score);
  renderComponents(document.getElementById('components'), score);
  renderAccountAges(document.getElementById('ages'), ages, !acc.known);
}

function renderComponents(el, score) {
  if (!score) {
    el.innerHTML = '<span class="muted">This stream hasn’t been scored yet. Live streams are scored after an hour, then every 15 minutes.</span>';
    return;
  }
  const comps = score.components || {};
  const keys = Object.keys(WEIGHTS).sort((a, b) => {
    const ca = comps[a] || {}, cb = comps[b] || {};
    return (cb.available ? cb.strength * WEIGHTS[b] : -1) - (ca.available ? ca.strength * WEIGHTS[a] : -1);
  });
  el.innerHTML = keys.map((k) => {
    const c = comps[k] || {};
    const [name, desc] = COMPONENTS[k];
    const details = c.values ? Object.entries(c.values).map(([key, v]) => `${key.replaceAll('_', ' ')} ${Math.abs(v) < 10 ? v.toFixed(2) : n(v)}`).join(' · ') : '';
    return `<div class="comp ${c.available ? '' : 'na'}">
      <div class="name">${name}<small>${desc}</small></div>
      ${c.available ? `<div class="bar" role="img" aria-label="strength ${pct(c.strength)}"><div style="width:${Math.max(1, c.strength * 100)}%"></div></div>`
        : `<div class="muted">not available${c.reason ? ` – ${esc(c.reason)}` : ''}</div>`}
      <div class="num r" style="text-align:right">${c.available ? pct(c.strength) : ''}<div class="muted" style="font-size:11px">weight ${WEIGHTS[k]}</div></div>
      ${c.available && c.reason ? `<div class="detail">${esc(c.reason)}</div>` : ''}
      ${details ? `<div class="detail">${esc(details)}</div>` : ''}
    </div>`;
  }).join('');
}

// ---------- charts ----------

const REPLAY_SPEEDS = [[10, '10 min/s'], [30, '30 min/s'], [60, '1 h/s'], [120, '2 h/s']];

// Three stacked panels on one time axis. Each has its own y-scale, so there
// is never a second axis on one chart. A replay control sweeps the shared
// crosshair across the stream with live counters.
function renderCharts(container, data) {
  const all = [...data.viewers, ...data.chatters, ...data.follows].map((p) => p[0]);
  if (!all.length) {
    container.innerHTML = '<div class="empty">No samples recorded for this stream.</div>';
    return;
  }
  const t0 = Math.min(...all);
  const t1 = Math.max(...all);
  const panels = [
    { key: 'viewers', label: 'Concurrent viewers', note: 'Helix count; dashed line is the 31-minute rolling median', height: 220,
      series: [
        { name: 'Viewers', color: 'var(--series-1)', points: data.smooth, raw: data.viewers },
        { name: 'Baseline', color: 'var(--baseline)', points: data.baseline, dashed: true },
      ] },
    { key: 'chat', label: 'Unique chatters per minute', height: 120,
      series: [{ name: 'Chatters', color: 'var(--series-2)', points: data.chatters }] },
    { key: 'follows', label: 'Follows per minute', note: 'from follower-total changes', height: 120, bars: true,
      series: [{ name: 'Follows', color: 'var(--series-3)', points: data.follows }] },
  ];
  const onsets = data.onsets || [];
  const speedOpts = REPLAY_SPEEDS.map(([v, t]) => `<option value="${v}"${v === 30 ? ' selected' : ''}>${t}</option>`).join('');
  container.innerHTML = `
    <div class="replay" role="group" aria-label="Replay the stream">
      <button type="button" class="play" aria-label="Play replay">▶ Replay</button>
      <label class="speed">Speed <select aria-label="Replay speed">${speedOpts}</select></label>
      <input type="range" class="scrub" min="${t0}" max="${t1}" step="60" value="${t0}" aria-label="Replay position">
      <div class="counters" aria-live="off">
        <div><span class="cv num" data-k="time">–</span><span class="cl">time</span></div>
        <div><span class="cv num" data-k="viewers">–</span><span class="cl"><i style="background:var(--series-1)"></i>viewers</span></div>
        <div><span class="cv num" data-k="chatters">–</span><span class="cl"><i style="background:var(--series-2)"></i>chatters / min</span></div>
        <div><span class="cv num" data-k="perk">–</span><span class="cl">chatters per 1k viewers</span></div>
      </div>
    </div>
    <p class="caption">Replay sweeps the stream at the chosen pace. Watch chatters per 1,000 viewers: when viewers jump and that number
      drops, the new viewers are not chatting. Bot viewers produce that pattern, but so do embeds and front-page placement, so it is a
      signal, not proof.</p>
    <div class="legend">
      <span><i style="background:var(--series-1)"></i>Viewers</span>
      <span><i class="dash"></i>Viewer baseline</span>
      ${onsets.length ? '<span><svg class="key" width="12" height="12" aria-hidden="true"><circle cx="6" cy="6" r="3.5" class="onset-dot"/></svg>Spike onset (detected)</span>' : ''}
      <span><i style="background:var(--series-2)"></i>Unique chatters / min</span>
      <span><i style="background:var(--series-3)"></i>Follows / min</span>
      <span><i style="background:var(--text-muted)"></i>Raid or category change</span>
    </div>`;

  const margin = { l: 48, r: 12, t: 6, b: 22 };
  const crosshairs = [];
  const width = container.clientWidth - 32 || 900;
  const x = (t) => margin.l + ((t - t0) / Math.max(1, t1 - t0)) * (width - margin.l - margin.r);

  for (const p of panels) {
    const wrap = document.createElement('div');
    wrap.className = 'chart';
    wrap.innerHTML = `<div class="label">${p.label}${p.note ? `<small>${p.note}</small>` : ''}</div>`;
    container.appendChild(wrap);
    const h = p.height;
    const svg = el('svg', { viewBox: `0 0 ${width} ${h}`, height: h, role: 'img',
      'aria-label': p.key === 'viewers' && onsets.length ? `${p.label}, with ${onsets.length} detected spike onsets` : p.label });
    wrap.appendChild(svg);
    const vals = p.series.flatMap((s) => s.points.map((pt) => pt[1])).filter((v) => v != null);
    const minV = Math.min(0, ...vals);
    const ticks = niceTicks(Math.max(1, ...vals));
    const maxV = ticks[ticks.length - 1];
    const y = (v) => h - margin.b - ((v - minV) / (maxV - minV || 1)) * (h - margin.t - margin.b);
    const axis = el('g', { class: 'axis' }, svg);
    for (const tv of ticks) {
      el('line', { class: 'gridline', x1: margin.l, x2: width - margin.r, y1: y(tv), y2: y(tv) }, axis);
      el('text', { x: margin.l - 6, y: y(tv) + 4, 'text-anchor': 'end' }, axis).textContent = compact(tv);
    }
    // hourly ticks
    const hour = 3600;
    for (let tt = Math.ceil(t0 / hour) * hour; tt <= t1; tt += hour * Math.max(1, Math.round((t1 - t0) / hour / (width < 600 ? 5 : 10)))) {
      el('text', { x: x(tt), y: h - 4, 'text-anchor': 'middle' }, axis).textContent = time(tt * 1000);
    }
    let labelEnd = -Infinity;
    for (const e of data.events) {
      const ex = x(new Date(e.ts).getTime() / 1000);
      el('line', { class: 'event-line', x1: ex, x2: ex, y1: margin.t, y2: h - margin.b }, svg);
      if (p.key === 'viewers' && ex > labelEnd) {
        const label = e.kind === 'raid_in' ? `raid ${e.detail.from ?? ''} (${n(e.detail.viewers)})` : `→ ${e.detail.to ?? ''}`;
        const t = el('text', { class: 'event-label', x: ex + 4, y: margin.t + 10 }, svg);
        t.textContent = label;
        labelEnd = ex + 4 + t.getComputedTextLength() + 8;
      }
    }
    for (const s of p.series) {
      const pts = s.points.filter((pt) => pt[1] != null);
      if (!pts.length) continue;
      if (p.bars) {
        const bw = Math.max(1, (width - margin.l - margin.r) / Math.max(60, (t1 - t0) / 60) - 1);
        for (const pt of pts) {
          const top = y(Math.max(0, pt[1])), base = y(Math.min(0, pt[1]));
          el('rect', { x: x(pt[0]) - bw / 2, y: top, width: bw, height: Math.max(0.5, base - top), rx: Math.min(2, bw / 2), fill: s.color }, svg);
        }
      } else {
        const d = pts.map((pt, i) => `${i ? 'L' : 'M'}${x(pt[0]).toFixed(1)},${y(pt[1]).toFixed(1)}`).join('');
        const path = el('path', { d, class: `line${s.dashed ? '' : ' draw'}`, stroke: s.color }, svg);
        if (s.dashed) path.setAttribute('stroke-dasharray', '4 3');
        else requestAnimationFrame(() => path.style.setProperty('--len', path.getTotalLength()));
      }
    }
    if (p.key === 'viewers') {
      // Detected onsets, placed on the smoothed viewer line.
      for (const o of onsets) {
        const v = nearest(data.smooth, o.ts, 120);
        if (!v) continue;
        el('circle', { cx: x(o.ts), cy: y(v[1]), r: 3.5, class: 'onset-dot' }, svg);
      }
    }
    const cross = el('line', { class: 'crosshair', y1: margin.t, y2: h - margin.b, visibility: 'hidden' }, svg);
    crosshairs.push(cross);
    const hit = el('rect', { x: margin.l, y: 0, width: width - margin.l - margin.r, height: h, fill: 'transparent' }, svg);
    hit.addEventListener('mousemove', (ev) => {
      if (replay.playing) return;
      const t = t0 + ((svgX(svg, width, ev) - margin.l) / (width - margin.l - margin.r)) * (t1 - t0);
      setCursor(t);
      const v = nearest(data.viewers, t), b = nearest(data.baseline, t), c = nearest(data.chatters, t), f = nearest(data.follows, t);
      const onset = onsets.find((o) => Math.abs(o.ts - t) <= 90);
      showTooltip(ev, `<div class="t">${time(t * 1000)}${onset ? ` · spike onset (+${Math.round(onset.rel * 100)}%)` : ''}</div>
        ${tipRow('var(--series-1)', 'Viewers', v ? n(v[1]) : '–')}
        ${tipRow('var(--baseline)', 'Baseline', b ? n(b[1]) : '–')}
        ${tipRow('var(--series-2)', 'Chatters / min', c ? n(c[1]) : '–')}
        ${tipRow('var(--series-3)', 'Follows / min', f ? f[1].toFixed(1) : '–')}`);
    });
    hit.addEventListener('mouseleave', () => {
      if (replay.playing) return;
      for (const c of crosshairs) c.setAttribute('visibility', 'hidden');
      hideTooltip();
    });
  }

  // ----- replay -----
  const btn = container.querySelector('.play');
  const speedSel = container.querySelector('.speed select');
  const scrub = container.querySelector('.scrub');
  const counter = Object.fromEntries([...container.querySelectorAll('.cv')].map((e) => [e.dataset.k, e]));
  const replay = { playing: false, t: t0, last: 0, raf: 0 };

  function setCursor(t) {
    for (const c of crosshairs) { c.setAttribute('x1', x(t)); c.setAttribute('x2', x(t)); c.setAttribute('visibility', 'visible'); }
    const v = nearest(data.smooth, t, 120), c = nearest(data.chatters, t);
    counter.time.textContent = time(t * 1000);
    counter.viewers.textContent = v ? n(v[1]) : '–';
    counter.chatters.textContent = c ? n(c[1]) : '–';
    counter.perk.textContent = v && c && v[1] > 0 ? dec((c[1] / v[1]) * 1000) : '–';
  }
  function stop() {
    replay.playing = false;
    cancelAnimationFrame(replay.raf);
    btn.textContent = '▶ Replay';
    btn.setAttribute('aria-label', 'Play replay');
  }
  function frame(now) {
    if (!replay.playing) return;
    const dt = replay.last ? (now - replay.last) / 1000 : 0;
    replay.last = now;
    replay.t = Math.min(t1, replay.t + dt * Number(speedSel.value) * 60);
    scrub.value = replay.t;
    setCursor(replay.t);
    if (replay.t >= t1 || !container.isConnected) { stop(); return; }
    replay.raf = requestAnimationFrame(frame);
  }
  btn.addEventListener('click', () => {
    if (replay.playing) { stop(); return; }
    hideTooltip();
    if (replay.t >= t1) replay.t = t0;
    replay.playing = true;
    replay.last = 0;
    btn.textContent = '❚❚ Pause';
    btn.setAttribute('aria-label', 'Pause replay');
    replay.raf = requestAnimationFrame(frame);
  });
  scrub.addEventListener('input', () => {
    stop();
    replay.t = Number(scrub.value);
    setCursor(replay.t);
  });
  setCursor(t0);
  for (const c of crosshairs) c.setAttribute('visibility', 'hidden');
  if (reducedMotion()) speedSel.value = '10';
}

// ---------- about ----------

function aboutView() {
  app.innerHTML = `<div class="prose">
    <h1>How scoring works</h1>
    <p class="sub">Every channel that reaches the Twitch top 200 is tracked from then on. Each minute the collector records its
      viewer count (Twitch Helix API), its chat activity (anonymous IRC), and its follower total.</p>
    <p>When a stream ends, eleven detectors each rate one kind of evidence from 0 to 1. They are combined with fixed weights
      (a weighted noisy-OR) into a quotient from 0 to 100. Live streams get a provisional score after an hour.</p>
    <ul>${Object.entries(COMPONENTS).map(([k, [name, desc]]) => `<li><b>${name}</b> (weight ${WEIGHTS[k]}): ${desc}.</li>`).join('')}</ul>
    <p><b>Confidence</b> reflects how many detectors had enough data and how long the stream was observed. A channel’s
      quotient is a confidence- and recency-weighted average of its last ten streams.</p>
    <h2>Limits</h2>
    <ul>
      <li>The weights are hand-set and have not been calibrated against confirmed cases.</li>
      <li>Twitch’s API refreshes viewer counts only every few minutes, so short spikes are blurred.</li>
      <li>Ad breaks, front-page placement, embeds, raids and chat restrictions all change these numbers for innocent reasons.
        The detectors account for raids, category changes and subscriber-only chat, but not the rest.</li>
      <li>Someone else can view-bot a channel to get it penalized. A high quotient says the data looks botted, not who did it.</li>
    </ul>
  </div>`;
}

route();
