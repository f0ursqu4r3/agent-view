import { Application, Container } from 'pixi.js';
import type { HostStatus } from '../../src/shared/schema';
import type { Touch, ViewAgent, ViewerStore } from '../store';
import { AgentsView } from './agents-view';
import { LineBatch, RingBatch, SpriteBatch } from './batch';
import { EffectsView } from './effects-view';
import { LabelPool } from './labels';
import { FONT, installFonts, makeTextures } from './textures';
import type { StageTextures } from './textures';
import type { TreeModel, TreeNode } from './tree-model';
import { TreeView } from './tree-view';

const BACKGROUND = 0x04060b;

type Frameable = { begin(): void; end?(): void };
type Parts = {
  app: Application;
  textures: StageTextures;
  treeView: TreeView;
  agentsView: AgentsView;
  effectsView: EffectsView;
  frameables: Frameable[];
};

/** Hosts in a stable order: the hub's list, then any host seen only in the tree or on an agent. */
export function hostOrder(listed: HostStatus[], treeHosts: TreeNode[], agents: ViewAgent[]): string[] {
  const out: string[] = [];
  const seen = new Set<string>();
  const add = (h: string): void => {
    if (seen.has(h)) return;
    seen.add(h);
    out.push(h);
  };
  for (const h of listed) add(h.label);
  for (const r of treeHosts) add(r.name);
  for (const a of agents) add(a.host);
  return out;
}

/**
 * The full-screen PixiJS stage: one growing radial tree per host, agents, and call effects.
 * The caller drives it: `noteTouch` and `fadeNodes` as the tree changes, then `render` once per frame.
 */
export class Stage {
  private parts: Parts | null = null;
  private reduced = false;
  private readonly clickFns: ((agentId: string) => void)[] = [];
  private readonly listeners: [string, (e: PointerEvent) => void][] = [];

  constructor(private readonly host: HTMLElement, private readonly store: ViewerStore, private readonly tree: TreeModel) {}

  async start(): Promise<void> {
    if (this.parts) return;
    const app = new Application();
    await app.init({
      resizeTo: this.host,
      background: BACKGROUND,
      antialias: true,
      resolution: Math.min(window.devicePixelRatio || 1, 2),
      autoDensity: true,
      autoStart: false,
      sharedTicker: false,
      preference: 'webgl',
      powerPreference: 'high-performance',
      eventFeatures: { move: false, globalMove: false, click: false, wheel: false },
    });
    app.canvas.classList.add('stage-canvas');
    this.host.appendChild(app.canvas);
    installFonts();
    const textures = makeTextures();

    const edges = new LineBatch(textures.line);
    const dots = new SpriteBatch(textures.dot);
    const nodeGlows = new SpriteBatch(textures.glow, { blend: 'add' });
    const glowLines = new LineBatch(textures.line, 'add');
    const links = new LineBatch(textures.line);
    const lines = new LineBatch(textures.line);
    const rings = new RingBatch();
    const trails = new LineBatch(textures.line);
    const sparks = new SpriteBatch(textures.glow, { blend: 'add' });
    const agentGlows = new SpriteBatch(textures.glow, { blend: 'add' });
    const cores = new SpriteBatch(textures.dot);
    const treeNames = new LabelPool<TreeNode | string>(FONT.sansBold, 11.5);
    const fileLabels = new LabelPool<TreeNode>(FONT.mono, 10);
    const initials = new LabelPool<string>(FONT.sansBold, 10.5);
    const agentNames = new LabelPool<string>(FONT.sans, 11);
    const activity = new LabelPool<string>(FONT.mono, 10);

    // Three render groups: the steady tree, the busy effects and agents, and the labels.
    const treeGroup = new Container({ isRenderGroup: true });
    treeGroup.addChild(edges.layer, dots.layer);
    const fxGroup = new Container({ isRenderGroup: true });
    fxGroup.addChild(nodeGlows.layer, glowLines.layer, links.layer, lines.layer, rings.layer, trails.layer, sparks.layer, agentGlows.layer, cores.layer);
    const labelGroup = new Container({ isRenderGroup: true });
    labelGroup.addChild(treeNames.layer, fileLabels.layer, initials.layer, agentNames.layer, activity.layer);
    app.stage.addChild(treeGroup, fxGroup, labelGroup);

    this.parts = {
      app,
      textures,
      treeView: new TreeView({ edges, dots, glows: nodeGlows, names: treeNames, files: fileLabels }),
      agentsView: new AgentsView({ links, trails, glows: agentGlows, cores, rings, initials, names: agentNames, activity }),
      effectsView: new EffectsView({ lines, glowLines, sparks, rings }),
      frameables: [edges, dots, nodeGlows, glowLines, links, lines, rings, trails, sparks, agentGlows, cores, treeNames, fileLabels, initials, agentNames, activity],
    };

    const at = (e: PointerEvent): string | null => {
      const r = app.canvas.getBoundingClientRect();
      return this.parts?.agentsView.hit(e.clientX - r.left, e.clientY - r.top) ?? null;
    };
    this.listen('pointerdown', (e) => {
      const id = at(e);
      if (id !== null) for (const fn of this.clickFns) fn(id);
    });
    this.listen('pointermove', (e) => {
      app.canvas.style.cursor = at(e) === null ? '' : 'pointer';
    });
  }

  /** Record a touch that `tree.touch` placed on `node`: confirmed touches glow, unattributed ones flicker grey. */
  noteTouch(node: TreeNode, t: Touch): void {
    this.parts?.treeView.noteTouch(node, t);
  }

  /** Nodes `tree.prune` removed: they fade out over 600 ms. */
  fadeNodes(nodes: TreeNode[], now: number): void {
    this.parts?.treeView.fade(nodes, now);
  }

  render(now: number, dt: number): void {
    const p = this.parts;
    if (!p) return;
    const { width, height } = p.app.screen;
    for (const f of p.frameables) f.begin();
    // Lane order puts each parent before its sub-agents, so links and spawn points use this frame's parent.
    const rank = new Map(this.store.laneOrder(now).map((id, i) => [id, i]));
    const agents = this.store.agents(now).sort((a, b) => (rank.get(a.id) ?? 1e9) - (rank.get(b.id) ?? 1e9));
    const treeHosts = this.tree.hosts();
    const roots = new Map(treeHosts.map((r) => [r.name, r]));
    p.treeView.draw(now, dt, width, height, hostOrder(this.store.hosts(), treeHosts, agents), roots, this.reduced);
    p.agentsView.update(now, dt, agents, this.store, this.tree, p.treeView, this.reduced);
    p.agentsView.drawFocus(this.store.focus, now, this.reduced);
    p.effectsView.draw(now, this.store, this.tree, p.treeView, p.agentsView, this.reduced);
    for (const f of p.frameables) f.end?.();
    p.app.render();
  }

  onAgentClick(fn: (agentId: string) => void): void {
    this.clickFns.push(fn);
  }

  setReducedMotion(on: boolean): void {
    this.reduced = on;
  }

  /** The WebGL renderer string, for the FPS overlay and measurements. */
  rendererInfo(): string {
    const r = this.parts?.app.renderer as unknown as { name?: string; gl?: WebGLRenderingContext } | undefined;
    const gl = r?.gl;
    if (!gl) return r?.name ?? 'none';
    const ext = gl.getExtension('WEBGL_debug_renderer_info');
    return String(ext ? gl.getParameter(ext.UNMASKED_RENDERER_WEBGL) : gl.getParameter(gl.RENDERER));
  }

  get canvas(): HTMLCanvasElement | null {
    return this.parts?.app.canvas ?? null;
  }

  destroy(): void {
    const p = this.parts;
    if (!p) return;
    this.parts = null;
    for (const [type, fn] of this.listeners) p.app.canvas.removeEventListener(type, fn as EventListener);
    this.listeners.length = 0;
    this.clickFns.length = 0;
    p.app.destroy({ removeView: true }, { children: true });
    for (const t of [p.textures.dot, p.textures.glow]) t.destroy(true);
  }

  private listen(type: 'pointerdown' | 'pointermove', fn: (e: PointerEvent) => void): void {
    this.parts!.app.canvas.addEventListener(type, fn);
    this.listeners.push([type, fn]);
  }
}
