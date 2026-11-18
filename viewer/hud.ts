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
 * The corner HUD: one line per host with its state, `SIMULATED` in demo mode,
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
    const sig = `${demo}|${JSON.stringify(hosts)}`;
    if (sig !== this.hostsSig) {
      this.hostsSig = sig;
      this.badge.hidden = !demo;
      this.renderHosts(hosts);
    }
    if (this.coverageOpen) this.renderCoverage(hosts);
  }

  private renderHosts(hosts: HostStatus[]): void {
    if (hosts.length === 0) {
      this.hosts.replaceChildren(el('div', 'hud-host', 'connecting'));
      return;
    }
    this.hosts.replaceChildren(...hosts.map((h) => {
      const line = el('div', `hud-host ${h.state}`);
      line.append(el('i', 'dot'), el('span', 'hud-label', h.label), el('span', 'hud-state', h.state));
      if (h.state === 'down' && h.detail) line.appendChild(el('span', 'hud-detail', h.detail));
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
