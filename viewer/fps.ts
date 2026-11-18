const WINDOW_MS = 10_000;

type FpsWindow = Window & { __fps?: number | null; __fpsReady?: boolean; __fpsRenderer?: string };

/**
 * Dev overlay for `?fps=1`: frames per second now and averaged over the last 10 s.
 * The average is also on `window.__fps` (null until 10 s of frames), for measurement scripts.
 */
export class FpsMeter {
  private readonly el: HTMLElement;
  private readonly frames: number[] = [];
  private head = 0;
  private lastText = 0;

  constructor(parent: HTMLElement, private readonly renderer: () => string) {
    this.el = document.createElement('div');
    this.el.className = 'fps';
    this.el.textContent = 'fps …';
    parent.appendChild(this.el);
    const w = window as FpsWindow;
    w.__fps = null;
    w.__fpsReady = false;
  }

  tick(t: number): void {
    this.frames.push(t);
    while (this.head < this.frames.length && t - this.frames[this.head]! > WINDOW_MS) this.head++;
    if (this.head > 1024) {
      this.frames.splice(0, this.head);
      this.head = 0;
    }
    const n = this.frames.length - this.head;
    const span = t - this.frames[this.head]!;
    const w = window as FpsWindow;
    const avg = n > 1 && span > 0 ? ((n - 1) / span) * 1000 : 0;
    if (span >= WINDOW_MS - 100) {
      w.__fps = avg;
      w.__fpsReady = true;
    }
    if (t - this.lastText < 500) return;
    this.lastText = t;
    let recent = 0;
    for (let i = this.frames.length - 1; i > this.head && t - this.frames[i]! <= 1000; i--) recent++;
    w.__fpsRenderer = this.renderer();
    this.el.textContent = `${recent} fps · 10 s avg ${w.__fpsReady ? avg.toFixed(1) : '…'} · ${w.__fpsRenderer}`;
  }
}
