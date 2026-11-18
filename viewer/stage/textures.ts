import { BitmapFont, Texture } from 'pixi.js';

/** Font stacks, as in the layout mockup. */
export const SANS = '-apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif';
export const MONO = 'ui-monospace, SFMono-Regular, Menlo, monospace';

/** Installed bitmap font names. Glyphs are white; labels take their color from `tint`. */
export const FONT = { sans: 'av-sans', sansBold: 'av-sans-bold', mono: 'av-mono' } as const;

export type StageTextures = {
  /** White disc, 64 px. */
  dot: Texture;
  /** White radial gradient, 128 px (M0 glow: tinted, blendMode 'add'). */
  glow: Texture;
  /** White pixel, for lines. */
  line: Texture;
};

/** Size in texture pixels of each texture above, for scaling to screen sizes. */
export const TEX_SIZE = { dot: 64, glow: 128 } as const;

function canvasTexture(size: number, draw: (g: CanvasRenderingContext2D, s: number) => void): Texture {
  const c = document.createElement('canvas');
  c.width = c.height = size;
  draw(c.getContext('2d')!, size);
  return Texture.from(c);
}

export function makeTextures(): StageTextures {
  const dot = canvasTexture(TEX_SIZE.dot, (g, s) => {
    g.fillStyle = '#fff';
    g.beginPath();
    g.arc(s / 2, s / 2, s / 2 - 1, 0, Math.PI * 2);
    g.fill();
  });
  const glow = canvasTexture(TEX_SIZE.glow, (g, s) => {
    const grd = g.createRadialGradient(s / 2, s / 2, 0, s / 2, s / 2, s / 2);
    grd.addColorStop(0, 'rgba(255,255,255,0.95)');
    grd.addColorStop(0.25, 'rgba(255,255,255,0.45)');
    grd.addColorStop(1, 'rgba(255,255,255,0)');
    g.fillStyle = grd;
    g.fillRect(0, 0, s, s);
  });
  return { dot, glow, line: Texture.WHITE };
}

let installed = false;

/** Glyphs render at twice the label size, so labels stay sharp on 2x screens. Missing glyphs are added on demand. */
export function installFonts(): void {
  if (installed) return;
  installed = true;
  const chars = [[' ', '~']] as string[][];
  BitmapFont.install({ name: FONT.sans, style: { fontFamily: SANS, fontSize: 12, fill: 0xffffff }, chars, resolution: 2 });
  BitmapFont.install({ name: FONT.sansBold, style: { fontFamily: SANS, fontSize: 12, fontWeight: '600', fill: 0xffffff }, chars, resolution: 2 });
  BitmapFont.install({ name: FONT.mono, style: { fontFamily: MONO, fontSize: 11, fill: 0xffffff }, chars, resolution: 2 });
}
