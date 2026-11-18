// Placeholder viewer. It is replaced by the stage and drawer in later tasks.
export {};
const status = document.getElementById('status')!;
const hosts = new Set<string>();
let events = 0;

const render = (): void => {
  status.textContent = `Agent View: ${hosts.size} host(s), ${events} event(s)`;
};

const ws = new WebSocket(`ws://${location.host}/ws`);
ws.onmessage = (m) => {
  const msg = JSON.parse(String(m.data)) as { type?: string; host?: string; hosts?: { label: string }[] };
  if (msg.type === 'hosts') {
    for (const h of msg.hosts ?? []) hosts.add(h.label);
  } else {
    events++;
    if (msg.host) hosts.add(msg.host);
  }
  render();
};
ws.onclose = () => { status.textContent = 'disconnected'; };
