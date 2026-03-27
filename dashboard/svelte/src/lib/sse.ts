const API_BASE = '/_oberwatch/api/v1';

export function connectStream(onEvent: (event: string, data: unknown) => void): EventSource {
  const source = new EventSource(`${API_BASE}/stream`);
  source.addEventListener('cost_update', (event) => {
    const message = event as MessageEvent;
    onEvent('cost_update', JSON.parse(message.data));
  });
  source.addEventListener('budget_alert', (event) => {
    const message = event as MessageEvent;
    onEvent('budget_alert', JSON.parse(message.data));
  });
  source.addEventListener('agent_killed', (event) => {
    const message = event as MessageEvent;
    onEvent('agent_killed', JSON.parse(message.data));
  });
  return source;
}
