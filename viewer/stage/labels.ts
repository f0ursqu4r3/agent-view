import { BitmapText, Container } from 'pixi.js';

type Label = { text: BitmapText; seen: number; tint: number; ax: number; ay: number };

/**
 * Pooled BitmapText labels keyed by owner (M0: labels exist only while wanted, and are reused).
 * Per frame: `begin`, `show` each wanted label, `end` releases the rest.
 */
export class LabelPool<K> {
  readonly layer = new Container();
  private readonly live = new Map<K, Label>();
  private readonly free: BitmapText[] = [];
  private frame = 0;

  constructor(private readonly font: string, private readonly size: number) {}

  begin(): void {
    this.frame++;
  }

  show(key: K, text: string, x: number, y: number, color: number, alpha: number, ax = 0, ay = 0): void {
    if (alpha <= 0.01) return;
    let l = this.live.get(key);
    if (!l) {
      let t = this.free.pop();
      if (t) {
        t.visible = true;
      } else {
        t = new BitmapText({ text, style: { fontFamily: this.font, fontSize: this.size, fill: 0xffffff } });
        this.layer.addChild(t);
      }
      l = { text: t, seen: 0, tint: -1, ax: -1, ay: -1 };
      this.live.set(key, l);
    }
    l.seen = this.frame;
    const t = l.text;
    if (t.text !== text) t.text = text;
    if (l.ax !== ax || l.ay !== ay) {
      l.ax = ax;
      l.ay = ay;
      t.anchor.set(ax, ay);
    }
    if (l.tint !== color) {
      l.tint = color;
      t.tint = color;
    }
    t.position.set(Math.round(x), Math.round(y));
    t.alpha = alpha > 1 ? 1 : alpha;
  }

  end(): void {
    for (const [key, l] of this.live) {
      if (l.seen === this.frame) continue;
      l.text.visible = false;
      this.free.push(l.text);
      this.live.delete(key);
    }
  }

  get count(): number {
    return this.live.size;
  }

  destroy(): void {
    this.layer.destroy({ children: true });
    this.live.clear();
    this.free.length = 0;
  }
}
