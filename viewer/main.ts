import { Drawer, toggleFocus } from './drawer/drawer';
import { FpsMeter } from './fps';
import { makeGuard } from './guard';
import { Hud } from './hud';
import { connectHub } from './socket';
import type { HubSocket } from './socket';
import { Stage } from './stage/stage';
import { TreeModel } from './stage/tree-model';
import { ViewerStore } from './store';

const PRUNE_EVERY_MS = 5000;
/** A frame step is capped, so a tab back from the background does not jump. */
const MAX_DT_S = 0.05;

export type FrameHook = (now: number, dt: number) => void;
export type Viewer = { store: ViewerStore; tree: TreeModel; stage: Stage; socket: HubSocket; onFrame(fn: FrameHook): void };

/** Creates the store, tree, stage, and hub socket, and starts the frame loop. */
export async function boot(root: HTMLElement): Promise<Viewer> {
  const store = new ViewerStore();
  const tree = new TreeModel();
  const stage = new Stage(root, store, tree);
  await stage.start();

  const socket = connectHub(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`, {
    onEnvelope: (e) => store.apply(e),
    onHosts: (h, lingerMs) => store.setHosts(h, lingerMs),
  });

  const motion = window.matchMedia('(prefers-reduced-motion: reduce)');
  stage.setReducedMotion(motion.matches);
  motion.addEventListener('change', (e) => stage.setReducedMotion(e.matches));

  const drawerRoot = document.getElementById('drawer');
  const hudRoot = document.getElementById('hud');
  const drawer = drawerRoot ? new Drawer(drawerRoot, store) : null;
  const hud = hudRoot ? new Hud(hudRoot, store) : null;
  stage.onAgentClick((id) => toggleFocus(store, id));
  window.addEventListener('keydown', (e) => {
    if (e.ctrlKey || e.metaKey || e.altKey) return;
    const t = e.target as HTMLElement | null;
    if (t && (t.tagName === 'INPUT' || t.tagName === 'TEXTAREA' || t.tagName === 'SELECT' || t.isContentEditable)) return;
    if (e.key === 'Escape') store.setFocus(null);
    else if (e.key === 't' || e.key === 'T') drawer?.toggle();
    else if (e.key === 'c' || e.key === 'C') hud?.toggleCoverage();
  });

  const fps = new URLSearchParams(location.search).get('fps') === '1' ? new FpsMeter(document.body, () => stage.rendererInfo()) : null;
  const hooks: FrameHook[] = [];
  const viewer: Viewer = { store, tree, stage, socket, onFrame: (fn) => hooks.push(fn) };
  let exposed = false;
  let lastT = performance.now();
  let lastPrune = Date.now();
  const guard = makeGuard();

  const frame = (t: number): void => {
    // The next frame is booked first, and each subsystem runs guarded: one exception never freezes the viewer.
    requestAnimationFrame(frame);
    const dt = Math.min(MAX_DT_S, Math.max(0, (t - lastT) / 1000));
    lastT = t;
    const now = Date.now();
    guard('resnapshot', () => { if (store.takeResnapshotRequest(now)) socket.send({ type: 'resnapshot' }); });
    let touches: ReturnType<ViewerStore['takeTouches']> = [];
    guard('store', () => { touches = store.takeTouches(now); });
    for (const touch of touches) {
      guard('tree', () => {
        const node = tree.touch(touch);
        if (node) stage.noteTouch(node, touch);
      });
    }
    if (now - lastPrune >= PRUNE_EVERY_MS) {
      lastPrune = now;
      guard('prune', () => stage.fadeNodes(tree.prune(now), now));
    }
    guard('layout', () => tree.layout(dt));
    guard('stage', () => stage.render(now, dt));
    guard('drawer', () => drawer?.render(now));
    guard('hud', () => hud?.render());
    for (const h of hooks) guard('hook', () => h(now, dt));
    guard('fps', () => fps?.tick(t));
    if (!exposed && store.isDemo()) {
      // Demo only: the smoke test reads the store through this.
      exposed = true;
      (window as unknown as { __agentView: unknown }).__agentView = { store, tree, stage };
    }
  };
  requestAnimationFrame(frame);
  return viewer;
}

void boot(document.getElementById('stage')!);
