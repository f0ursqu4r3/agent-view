import { Container, Graphics, Sprite } from 'pixi.js';
import type { BLEND_MODES, Texture } from 'pixi.js';

/**
 * Immediate-mode sprite pool: call `begin`, emit sprites for this frame, call `end`.
 * Sprites are reused in emit order, and unused ones are hidden, so a steady scene keeps its structure.
 */
export class SpriteBatch {
  readonly layer = new Container();
  private readonly sprites: Sprite[] = [];
  /** Last tint per sprite: the tint setter parses colors, so skip unchanged ones. */
  private readonly tints: number[] = [];
  private used = 0;
  private shown = 0;

  constructor(
    private readonly texture: Texture,
    private readonly opts: { anchorX?: number; anchorY?: number; blend?: BLEND_MODES } = {},
  ) {}

  begin(): void {
    this.used = 0;
  }

  end(): void {
    for (let i = this.used; i < this.shown; i++) this.sprites[i]!.visible = false;
    this.shown = this.used;
  }

  /** A sprite of the texture's own size scaled by (sx, sy), centered on its anchor at (x, y). */
  put(x: number, y: number, sx: number, sy: number, color: number, alpha: number, rotation = 0): void {
    if (alpha <= 0.003) return;
    const i = this.used++;
    let s = this.sprites[i];
    if (!s) {
      s = new Sprite(this.texture);
      s.anchor.set(this.opts.anchorX ?? 0.5, this.opts.anchorY ?? 0.5);
      if (this.opts.blend) s.blendMode = this.opts.blend;
      this.sprites.push(s);
      this.tints.push(-1);
      this.layer.addChild(s);
    }
    if (i >= this.shown) s.visible = true;
    s.position.set(x, y);
    s.scale.set(sx, sy);
    s.rotation = rotation;
    s.alpha = alpha > 1 ? 1 : alpha;
    if (this.tints[i] !== color) {
      this.tints[i] = color;
      s.tint = color;
    }
  }

  get count(): number {
    return this.used;
  }

  destroy(): void {
    this.layer.destroy({ children: true });
    this.sprites.length = 0;
  }
}

/** Lines from a 1x1 white texture: anchor at the left middle, scaled to length and width. */
export class LineBatch extends SpriteBatch {
  constructor(texture: Texture, blend?: BLEND_MODES) {
    super(texture, { anchorX: 0, anchorY: 0.5, blend });
  }

  line(x0: number, y0: number, x1: number, y1: number, width: number, color: number, alpha: number): void {
    const dx = x1 - x0;
    const dy = y1 - y0;
    const len = Math.sqrt(dx * dx + dy * dy);
    if (len < 0.5) return;
    this.put(x0, y0, len, width, color, alpha, Math.atan2(dy, dx));
  }

  /**
   * Dashes from (x0, y0) to (x1, y1). `phase` in pixels shifts the pattern along the line, toward the end.
   * Long lines get a longer period, so one line never takes more than `maxDashes` sprites.
   */
  dashed(
    x0: number, y0: number, x1: number, y1: number, width: number, color: number, alpha: number,
    dash = 4, gap = 5, phase = 0, maxDashes = 40,
  ): void {
    const dx = x1 - x0;
    const dy = y1 - y0;
    const len = Math.sqrt(dx * dx + dy * dy);
    if (len < 0.5) return;
    let period = dash + gap;
    let on = dash;
    if (len / period > maxDashes) {
      const k = len / maxDashes / period;
      period *= k;
      on *= k;
    }
    const ux = dx / len;
    const uy = dy / len;
    const rot = Math.atan2(dy, dx);
    let t = (((phase % period) + period) % period) - period;
    for (; t < len; t += period) {
      const a = t < 0 ? 0 : t;
      const b = t + on > len ? len : t + on;
      if (b - a < 0.5) continue;
      this.put(x0 + ux * a, y0 + uy * a, b - a, width, color, alpha, rot);
    }
  }
}

/** Stroked circles, redrawn each frame. Few at a time (pulses, edit rings, flashes, focus). */
export class RingBatch {
  readonly layer = new Graphics();

  begin(): void {
    this.layer.clear();
  }

  ring(x: number, y: number, r: number, width: number, color: number, alpha: number): void {
    if (alpha <= 0.003 || r <= 0) return;
    this.layer.circle(x, y, r).stroke({ width, color, alpha: alpha > 1 ? 1 : alpha });
  }

  destroy(): void {
    this.layer.destroy();
  }
}
