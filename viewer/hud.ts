import type { Coverage, HostStatus } from '../src/shared/schema';
import type { ViewerStore } from './store';

export const ACCURACY_NOTE =
  'Only recorded tool calls move agents. Grey flickers are file changes with no recorded author. Coverage is partial.';

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

/** A short note on the sources of one host that are not ok, such as `1 source down, 2 sources degraded`. Null when all are ok. */
export function sourceSummary(c: Coverage | undefined): string | null {
  if (!c) return null;
  const counts = { down: 0, degraded: 0 };
  for (const s of Object.values(c.sources)) if (s.state === 'down' || s.state === 'degraded') counts[s.state]++;
  const parts = (['down', 'degraded'] as const).filter((k) => counts[k] > 0)
    .map((k) => `${counts[k]} source${counts[k] === 1 ? '' : 's'} ${k}`);
  return parts.length > 0 ? parts.join(', ') : null;
}

function row(label: string, value: string): HTMLElement {
  const r = el('div', 'cov-row');
  r.append(el('span', 'cov-key', label), el('span', 'cov-val', value));
  return r;
}

function list(label: string, items: string[]): HTMLElement {
  const r = el('div', 'cov-row');
  r.appendChild(el('span', 'cov-key', label));
  const v = el('span', 'cov-val');
  if (items.length === 0) v.textContent = 'none';
  else for (const it of items) v.appendChild(el('div', 'cov-path', it));
  r.appendChild(v);
  return r;
}

/**
 * The corner HUD: one line per host with its state and a note on sources that are not ok, `SIMULATED` in demo mode,
 * and the `#coverage` panel (key `C`). Every string from agent data goes through `textContent`.
 */
export class Hud {
  private readonly hosts: HTMLElement;
  private readonly badge: HTMLElement;
  private readonly coverage: HTMLElement;
  private coverageOpen = false;
  private hostsSig = '';
  private coverageSig = '';

  constructor(private readonly root: HTMLElement, private readonly store: ViewerStore) {
    this.badge = el('div', 'hud-badge', 'SIMULATED');
    this.badge.hidden = true;
    this.hosts = el('div', 'hud-hosts');
    this.coverage = el('section', 'coverage');
    this.coverage.id = 'coverage';
    this.coverage.hidden = true;
    this.coverage.setAttribute('aria-label', 'Coverage');
    root.append(this.badge, this.hosts, this.coverage);
  }

  toggleCoverage(): void {
    this.coverageOpen = !this.coverageOpen;
    this.coverage.hidden = !this.coverageOpen;
    this.coverageSig = '';
    if (this.coverageOpen) this.renderCoverage(this.store.hosts());
  }

  render(): void {
    const hosts = this.store.hosts();
    const demo = this.store.isDemo();
    const notes = hosts.map((h) => sourceSummary(this.store.coverage(h.label)));
    const sig = `${demo}|${JSON.stringify(hosts)}|${JSON.stringify(notes)}`;
    if (sig !== this.hostsSig) {
      this.hostsSig = sig;
      this.badge.hidden = !demo;
      this.renderHosts(hosts, notes);
    }
    if (this.coverageOpen) this.renderCoverage(hosts);
  }

  private renderHosts(hosts: HostStatus[], notes: (string | null)[]): void {
    if (hosts.length === 0) {
      this.hosts.replaceChildren(el('div', 'hud-host', 'connecting'));
      return;
    }
    this.hosts.replaceChildren(...hosts.map((h, i) => {
      const line = el('div', `hud-host ${h.state}`);
      line.append(el('i', 'dot'), el('span', 'hud-label', h.label), el('span', 'hud-state', h.state));
      if (h.state === 'down' && h.detail) line.appendChild(el('span', 'hud-detail', h.detail));
      const note = notes[i];
      if (note) line.appendChild(el('span', 'hud-sources', note));
      return line;
    }));
  }

  private renderCoverage(hosts: HostStatus[]): void {
    const labels = hosts.map((h) => h.label);
    const data = labels.map((l) => this.store.coverage(l));
    const sig = JSON.stringify([labels, data]);
    if (sig === this.coverageSig) return;
    this.coverageSig = sig;
    const parts: HTMLElement[] = [el('h2', 'cov-title', 'Coverage')];
    labels.forEach((label, i) => {
      const sec = el('div', 'cov-host');
      sec.appendChild(el('h3', undefined, label));
      const c: Coverage | undefined = data[i];
      if (!c) {
        sec.appendChild(el('div', 'cov-empty', 'No report yet'));
      } else {
        sec.append(
          list('Log roots', c.logRoots),
          row('Logs open', `${c.logsOpen} of ${c.logsDiscovered} discovered`),
          list('Watch roots', c.watchRoots),
          row('Malformed records', String(c.malformedRecords)),
          row('FS events dropped', String(c.fsDropped)),
        );
        const sources = Object.entries(c.sources);
        sec.appendChild(list('Sources', sources.map(([name, s]) => `${name}: ${s.state}${s.detail ? ` (${s.detail})` : ''}`)));
      }
      parts.push(sec);
    });
    parts.push(el('p', 'cov-note', ACCURACY_NOTE));
    this.coverage.replaceChildren(...parts);
  }
}
