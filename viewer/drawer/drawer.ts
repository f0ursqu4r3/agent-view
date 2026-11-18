import type { ViewCall, ViewerStore } from '../store';
import { LANE_HEIGHT, TIMELINE_TOP, WINDOW_MS, agentColor, drawTimeline, laneAt, timelineLanes } from './timeline';

/** Matches the slide transition in `style.css`. The timeline keeps drawing until the drawer is out of sight. */
const SLIDE_MS = 450;
const OUTPUT_LINES = 2;
const ANSI = /\u001b\[[0-9;?]*[ -/]*[@-~]/g;
// eslint-disable-next-line no-control-regex
const CONTROL = /[\u0000-\u0008\u000b-\u001f\u007f]/g;

/** Focus an agent, or clear the focus when the same agent is clicked again. */
export function toggleFocus(store: ViewerStore, id: string): void {
  store.setFocus(store.focus === id ? null : id);
}

function el<K extends keyof HTMLElementTagNameMap>(tag: K, className?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (className) e.className = className;
  if (text !== undefined) e.textContent = text;
  return e;
}

/** The last non-empty output lines, without escape codes. Agent output is shown with `textContent` only. */
export function tailLines(output: string, n: number): string[] {
  const lines = output.replace(ANSI, '').replace(CONTROL, '').split(/\r?\n/).filter((l) => l.trim() !== '');
  return lines.slice(-n);
}

/** A cheap key for the command list. Output is capped, so its length alone stops changing: the tail is part of the key. */
export function commandSignature(focus: string | null, calls: ViewCall[], names: Map<string, { name: string }>): string {
  return [focus ?? '', ...calls.map((c) => {
    return `${c.agentId}|${c.callId}|${c.endMs}|${c.ok}|${c.exitCode}|${c.output.length}|${c.outputTruncated}|${names.get(c.agentId)?.name ?? ''}|${c.output.slice(-200)}`;
  })].join('\n');
}

export function callState(c: ViewCall): string {
  if (c.endMs === null) return 'running';
  if (c.exitCode !== undefined) return `exit ${c.exitCode}`;
  if (c.ok === true) return 'done';
  if (c.ok === false) return 'failed';
  return 'ended';
}

/**
 * The timeline drawer: the lane timeline on the left, the live command list on the right.
 * Closed at the start. The `open` class on `root` slides it in. `render` is called once per frame.
 */
export class Drawer {
  private isOpen = false;
  private closedAtMs = Number.NEGATIVE_INFINITY;
  private readonly handle: HTMLButtonElement;
  private readonly canvas: HTMLCanvasElement;
  private readonly ctx: CanvasRenderingContext2D | null;
  private readonly cmdTitle: HTMLElement;
  private readonly cmdList: HTMLElement;
  private cssW = 0;
  private cssH = 0;
  private dpr = 1;
  private lastNow = Date.now();
  private cmdSignature = '';

  constructor(private readonly root: HTMLElement, private readonly store: ViewerStore) {
    root.classList.remove('open');
    this.handle = el('button', 'handle');
    this.handle.type = 'button';
    this.handle.setAttribute('aria-controls', root.id || 'drawer');
    this.handle.append(el('i'), el('span', undefined, 'Timeline · press T'), el('i'));
    this.handle.addEventListener('click', (e) => {
      e.stopPropagation();
      this.toggle();
    });

    const timeline = el('div', 'timeline');
    this.canvas = el('canvas');
    this.canvas.setAttribute('aria-label', 'Agent timeline, last 16 seconds');
    timeline.appendChild(this.canvas);
    this.ctx = this.canvas.getContext('2d');

    const cmds = el('section', 'cmds');
    this.cmdTitle = el('h2', 'cmds-title', 'LIVE COMMANDS');
    this.cmdList = el('div', 'cmds-list');
    cmds.append(this.cmdTitle, this.cmdList);

    const body = el('div', 'drawer-body');
    body.append(timeline, cmds);
    root.append(this.handle, body);
    this.sync();

    this.canvas.addEventListener('click', (e) => {
      const y = e.clientY - this.canvas.getBoundingClientRect().top;
      const id = laneAt(y, TIMELINE_TOP, LANE_HEIGHT, timelineLanes(this.store, this.lastNow, this.cssH, LANE_HEIGHT));
      if (id !== null) toggleFocus(this.store, id);
    });
    this.canvas.addEventListener('pointermove', (e) => {
      const y = e.clientY - this.canvas.getBoundingClientRect().top;
      const id = laneAt(y, TIMELINE_TOP, LANE_HEIGHT, timelineLanes(this.store, this.lastNow, this.cssH, LANE_HEIGHT));
      this.canvas.style.cursor = id === null ? '' : 'pointer';
    });
  }

  get open(): boolean {
    return this.isOpen;
  }

  toggle(): void {
    this.isOpen = !this.isOpen;
    if (!this.isOpen) this.closedAtMs = Date.now();
    this.sync();
  }

  render(now: number): void {
    this.lastNow = now;
    if (!this.isOpen && Date.now() - this.closedAtMs > SLIDE_MS + 50) return;
    this.renderTimeline(now);
    this.renderCommands(now);
  }

  private sync(): void {
    this.root.classList.toggle('open', this.isOpen);
    this.handle.setAttribute('aria-expanded', String(this.isOpen));
    // A closed drawer shows only its handle. Its contents must not take focus.
    this.root.querySelector('.drawer-body')?.toggleAttribute('inert', !this.isOpen);
  }

  private resize(): boolean {
    const w = Math.floor(this.canvas.clientWidth);
    const h = Math.floor(this.canvas.clientHeight);
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    if (w === 0 || h === 0) return false;
    if (w !== this.cssW || h !== this.cssH || dpr !== this.dpr) {
      this.cssW = w;
      this.cssH = h;
      this.dpr = dpr;
      this.canvas.width = Math.round(w * dpr);
      this.canvas.height = Math.round(h * dpr);
    }
    return true;
  }

  private renderTimeline(now: number): void {
    const ctx = this.ctx;
    if (!ctx || !this.resize()) return;
    ctx.setTransform(this.dpr, 0, 0, this.dpr, 0, 0);
    drawTimeline(ctx, { w: this.cssW, h: this.cssH }, this.store, now, { laneHeight: LANE_HEIGHT, windowMs: WINDOW_MS, focus: this.store.focus });
  }

  private renderCommands(now: number): void {
    const focus = this.store.focus;
    const calls = this.store.commands(now, focus, 3);
    const names = new Map(this.store.agents(now).map((a) => [a.id, a]));
    const sig = commandSignature(focus, calls, names);
    if (sig === this.cmdSignature) return;
    this.cmdSignature = sig;

    this.cmdTitle.textContent = focus === null ? 'LIVE COMMANDS' : `COMMANDS · ${(names.get(focus)?.name ?? focus).toUpperCase()}`;
    const rows: HTMLElement[] = [];
    for (const c of calls) {
      const a = names.get(c.agentId);
      const running = c.endMs === null;
      const row = el('article', `cmd${running ? ' running' : ''}`);
      row.appendChild(el('div', 'cmd-line', `$ ${(c.command ?? c.rawName).replace(/\s+/g, ' ').trim()}`));
      const meta = el('div', 'cmd-meta');
      if (a) meta.style.color = agentColor(a);
      meta.textContent = `${a?.name ?? c.agentId} · ${callState(c)}`;
      row.appendChild(meta);
      if (c.outputTruncated) row.appendChild(el('div', 'cmd-out omitted', '[earlier output omitted]'));
      for (const line of tailLines(c.output, OUTPUT_LINES)) row.appendChild(el('div', 'cmd-out', line));
      rows.push(row);
    }
    if (rows.length === 0) rows.push(el('div', 'cmd-empty', focus === null ? 'No commands yet' : 'No commands for this agent'));
    this.cmdList.replaceChildren(...rows);
  }
}
