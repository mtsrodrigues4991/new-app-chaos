// Tipos e chamadas à API do backend.

export type Container = {
  id: string;
  name: string;
  image: string;
  state: string;
  ports: number[];
  networks: string[];
  service?: string;
};

export type Endpoint = {
  method: string;
  path: string;
  body?: string;
  headers?: Record<string, string>;
  weight: number;
};

export type ProfileType = 'ramp' | 'plateau' | 'spike';

export type Profile = { type: ProfileType; vus: number; durationSeconds: number };

export type Scenario = {
  id: string;
  name: string;
  target: { container: string; baseUrl: string };
  mode: 'load' | 'chaos' | 'both';
  endpoints: Endpoint[];
  profile: Profile;
};

export type RunStatus = 'pending' | 'running' | 'completed' | 'aborted' | 'failed';

export type Window = {
  t: number;
  p50: number;
  p95: number;
  p99: number;
  rps: number;
  errorRate: number;
  s2xx: number;
  s3xx: number;
  s4xx: number;
  s5xx: number;
  sErr: number;
  vus: number;
  cpu: number;
  memMb: number;
};

export type Run = {
  id: string;
  scenarioId: string;
  scenarioName: string;
  scenario: Scenario;
  status: RunStatus;
  error?: string;
  createdAt: string;
  startedAt?: string;
  endedAt?: string;
};

export type RunDetail = Run & { windows: Window[] };

export type LiveEvent =
  | { type: 'windows'; windows: Window[] }
  | { type: 'status'; status: RunStatus; error?: string };

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    const data = await res.json().catch(() => ({}));
    throw new Error(data.error ?? `HTTP ${res.status}`);
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  return res.json();
}

export const api = {
  containers: () => call<Container[]>('GET', '/api/containers'),
  scenarios: () => call<Scenario[]>('GET', '/api/scenarios'),
  scenario: (id: string) => call<Scenario>('GET', `/api/scenarios/${id}`),
  createScenario: (s: Omit<Scenario, 'id'>) => call<Scenario>('POST', '/api/scenarios', s),
  updateScenario: (s: Scenario) => call<Scenario>('PUT', `/api/scenarios/${s.id}`, s),
  deleteScenario: (id: string) => call<void>('DELETE', `/api/scenarios/${id}`),
  startRun: (scenarioId: string) => call<Run>('POST', `/api/scenarios/${scenarioId}/runs`),
  runs: (scenarioId?: string) => call<Run[]>('GET', `/api/runs${scenarioId ? `?scenarioId=${scenarioId}` : ''}`),
  run: (id: string) => call<RunDetail>('GET', `/api/runs/${id}`),
  abortRun: (id: string) => call<void>('POST', `/api/runs/${id}/abort`),
};

// Mesma conversão de perfil em stages do backend (internal/scenario), para a prévia no editor.
export function profileStages(p: Profile): { durationSeconds: number; target: number }[] {
  const d = p.durationSeconds;
  const n = p.vus;
  const part = (f: number) => Math.max(1, Math.floor(d * f));
  switch (p.type) {
    case 'ramp':
      return [{ durationSeconds: d, target: n }];
    case 'plateau': {
      const up = part(0.1);
      const down = part(0.1);
      return [
        { durationSeconds: up, target: n },
        { durationSeconds: d - up - down, target: n },
        { durationSeconds: down, target: 0 },
      ];
    }
    case 'spike': {
      const base = Math.max(1, Math.floor(n / 5));
      const [warm, su, sh, sd] = [part(0.3), part(0.05), part(0.2), part(0.05)];
      return [
        { durationSeconds: warm, target: base },
        { durationSeconds: su, target: n },
        { durationSeconds: sh, target: n },
        { durationSeconds: sd, target: base },
        { durationSeconds: d - warm - su - sh - sd, target: base },
      ];
    }
  }
}
