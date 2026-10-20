// Shared formatting, SVG and tooltip helpers for the dashboard charts.

export const SVG = 'http://www.w3.org/2000/svg';
const tooltip = document.getElementById('tooltip');

const fmt = new Intl.NumberFormat();
export const n = (v) => (v == null || Number.isNaN(v) ? '–' : fmt.format(Math.round(v)));
export const q = (v) => (v == null ? '–' : Math.round(v).toString());
export const pct = (v) => (v == null || Number.isNaN(v) ? '–' : `${Math.round(v * 100)}%`);
export const time = (d) => new Date(d).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
export const dateTime = (d) => new Date(d).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
export const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
export const dur = (a, b) => {
  const m = Math.round((new Date(b) - new Date(a)) / 60000);
  return `${Math.floor(m / 60)}h${String(m % 60).padStart(2, '0')}m`;
};
export const dec =(v, d = 1) => (v == null || Number.isNaN(v) ? '–' : v.toFixed(d));

export const reducedMotion = () => matchMedia('(prefers-reduced-motion: reduce)').matches;

// Moves to one of the app's own pages without a reload.
export function navigate(path, { replace = false } = {}) {
  if (path !== location.pathname + location.search) history[replace ? 'replaceState' : 'pushState'](null, '', path);
  dispatchEvent(new Event('navigate'));
}

export async function api(path) {
  const r = await fetch(`/api${path}`);
  if (!r.ok) throw Object.assign(new Error(`${r.status} ${await r.text()}`), { status: r.status });
  return r.json();
}

export function el(name, attrs = {}, parent) {
  const e = document.createElementNS(SVG, name);
  for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
  if (parent) parent.appendChild(e);
  return e;
}

export function niceStep(raw) {
  const mag = 10 ** Math.floor(Math.log10(raw));
  return [1, 2, 2.5, 5, 10].map((m) => m * mag).find((s) => s >= raw * 0.999);
}

export function niceTicks(max, count = 4, integer = false) {
  if (!(max > 0)) return [0];
  let step = niceStep(max / count);
  if (integer) step = Math.max(1, Math.ceil(step));
  const ticks = [];
  for (let v = 0; v <= max + step * 0.001; v += step) ticks.push(v);
  if (ticks[ticks.length - 1] < max) ticks.push(ticks[ticks.length - 1] + step);
  return ticks;
}

// Ticks spanning [min, max], which may be negative.
export function rangeTicks(min, max, count = 4) {
  if (!(max > min)) return [min];
  const step = niceStep((max - min) / count);
  const out = [];
  const end = Math.ceil(max / step - 1e-9) * step;
  for (let v = Math.floor(min / step + 1e-9) * step; v <= end + step * 0.001; v += step) out.push(Math.abs(v) < step * 1e-6 ? 0 : v);
  return out;
}

export function compact(v) {
  const trim = (s) => s.replace(/\.0$/, '');
  if (Math.abs(v) >= 1e6) return `${trim((v / 1e6).toFixed(1))}M`;
  if (Math.abs(v) >= 1e3) return `${trim((v / 1e3).toFixed(Math.abs(v) >= 1e4 ? 0 : 1))}k`;
  if (Math.abs(v) < 10 && v % 1) return String(+v.toFixed(Math.abs(v) < 1 ? 2 : 1));
  return String(Math.round(v));
}

// The sample nearest t (unix seconds) within maxGap seconds.
export function nearest(points, t, maxGap = 90) {
  let lo = 0, hi = points.length - 1;
  if (hi < 0) return null;
  while (lo < hi) {
    const mid = (lo + hi) >> 1;
    if (points[mid][0] < t) lo = mid + 1; else hi = mid;
  }
  const a = points[Math.max(0, lo - 1)], b = points[lo];
  const p = Math.abs(a[0] - t) < Math.abs(b[0] - t) ? a : b;
  return Math.abs(p[0] - t) <= maxGap ? p : null;
}

export function tipRow(color, label, val) {
  return `<div class="row"><span><i style="background:${color}"></i>${label}</span><b class="num">${val}</b></div>`;
}

export function showTooltip(ev, html) {
  tooltip.innerHTML = html;
  tooltip.hidden = false;
  const w = tooltip.offsetWidth, h = tooltip.offsetHeight;
  let left = ev.clientX + 14, top = ev.clientY + 14;
  if (left + w > innerWidth - 8) left = ev.clientX - w - 14;
  if (top + h > innerHeight - 8) top = ev.clientY - h - 14;
  tooltip.style.left = `${left}px`;
  tooltip.style.top = `${top}px`;
}

// Positions the tooltip beside an element, for keyboard focus.
export function showTooltipAt(target, html) {
  const r = target.getBoundingClientRect();
  showTooltip({ clientX: r.left + r.width / 2, clientY: r.top }, html);
}

export function hideTooltip() { tooltip.hidden = true; }

// Pointer x in viewBox units.
export function svgX(svg, width, ev) {
  const r = svg.getBoundingClientRect();
  return ((ev.clientX - r.left) / r.width) * width;
}
export function svgY(svg, height, ev) {
  const r = svg.getBoundingClientRect();
  return ((ev.clientY - r.top) / r.height) * height;
}

// A figure caption block: what to look for, and that it is a signal.
export function caption(text) {
  return `<p class="caption">${text}</p>`;
}

// Minute-grid preparation that mirrors detect.Prepare: samples are placed on
// a one-minute grid from the first sample, preserving repeated observations
// and missing polls. The baseline is a 31-minute
// rolling median, and rel = (viewers - baseline) / baseline.
export function prepareMinutes(points) {
  if (!points.length) return null;
  const start = Math.floor(points[0][0] / 60) * 60;
  const len = Math.floor((points[points.length - 1][0] - start) / 60) + 1;
  const raw = new Array(len).fill(NaN);
  for (const [t, v] of points) {
    const i = Math.floor((t - start) / 60);
    if (i >= 0 && i < len) raw[i] = v;
  }
  const covered = raw.filter((x) => !Number.isNaN(x)).length / len;
  const v = raw.slice();
  const base = new Array(len);
  for (let i = 0; i < len; i++) {
    const w = v.slice(Math.max(0, i - 15), i + 16).filter(Number.isFinite).sort((a, b) => a - b);
    const m = w.length;
    base[i] = m % 2 ? w[(m - 1) / 2] : (w[m / 2 - 1] + w[m / 2]) / 2;
  }
  const rel = v.map((x, i) => (base[i] > 0 ? (x - base[i]) / base[i] : 0));
  return { start, viewers: v, baseline: base, rel, coverage: covered };
}

// Autocorrelation at lag, as detect.Autocorrelation.
export function autocorrelation(v, lag) {
  const len = v.length;
  if (lag >= len) return 0;
  const finite = v.filter(Number.isFinite);
  if (!finite.length) return 0;
  const m = finite.reduce((a, b) => a + b, 0) / finite.length;
  let num = 0, den = 0;
  for (let i = 0; i < len; i++) {
    const d = v[i] - m;
    if (!Number.isFinite(d)) continue;
    den += d * d;
    if (i + lag < len && Number.isFinite(v[i + lag])) num += d * (v[i + lag] - m);
  }
  return den ? num / den : 0;
}

export function median(arr) {
  const s = arr.filter((x) => !Number.isNaN(x)).sort((a, b) => a - b);
  if (!s.length) return NaN;
  const m = s.length;
  return m % 2 ? s[(m - 1) / 2] : (s[m / 2 - 1] + s[m / 2]) / 2;
}
