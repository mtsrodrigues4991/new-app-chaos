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

// Deployment do Kubernetes candidato a alvo (quando o app roda no cluster).
export type DeploymentInfo = {
  namespace: string;
  name: string;
  replicas: number;
  ready: number;
  images: string[];
  urls: string[]; // Services que apontam para os pods
};

export type Platform = 'docker' | 'kubernetes';

export type Target = {
  platform?: Platform; // vazio = docker
  container?: string;
  namespace?: string;
  deployment?: string;
  baseUrl: string;
};

export function targetPlatform(t: Target): Platform {
  return t.platform || 'docker';
}

// Nome do alvo para mostrar: o container ou namespace/deployment.
export function targetName(t: Target): string {
  return targetPlatform(t) === 'kubernetes' ? `${t.namespace}/${t.deployment}` : t.container ?? '';
}

export type Health = { status: string; docker: boolean; kubernetes: boolean; observability?: boolean; namespace?: string; grafanaUrl?: string; authEnabled?: boolean };

// Login (modo nuvem). Com authEnabled false, o app fica aberto como no modo local.
export type User = { username: string; admin: boolean; createdAt: string };
export type Me = { authEnabled: boolean; user?: User };

// Disparado quando a sessão expira no meio do uso: o App volta para a tela de login.
export const UNAUTHORIZED_EVENT = 'nac:unauthorized';

export type Endpoint = {
  method: string;
  path: string;
  body?: string;
  headers?: Record<string, string>;
  weight: number;
};

export type ProfileType = 'ramp' | 'plateau' | 'spike';

// Com rate > 0 a carga é por taxa de chegada (modelo aberto): o perfil é em requisições por
// segundo e vus são os VUs pré-alocados. Sem rate, o perfil é em VUs (modelo fechado).
export type Profile = { type: ProfileType; vus: number; durationSeconds: number; rate?: number; warmupSeconds?: number };

export function openModel(p: Profile): boolean {
  return (p.rate ?? 0) > 0;
}

// Carga no pico, para mostrar: "200 req/s" ou "20 VUs".
export function profilePeak(p: Profile): string {
  return openModel(p) ? `${p.rate} req/s` : `${p.vus} VUs`;
}

export type Mode = 'load' | 'chaos' | 'both';

export type FaultType =
  | 'latency'
  | 'packet_loss'
  | 'cpu_stress'
  | 'memory_stress'
  | 'kill_restart'
  // Só no Kubernetes (Chaos Mesh).
  | 'io_latency'
  | 'io_fault'
  | 'dns_error'
  | 'http_delay'
  | 'http_abort';

// Deployment do cluster usado pelo alvo (ex.: o banco), onde uma falha pode ser aplicada.
export type Dependency = { namespace: string; deployment: string };

export type Fault = {
  id: string;
  type: FaultType;
  startSeconds: number;
  durationSeconds: number;
  params: Record<string, string>;
  // Kubernetes: falha aplicada numa dependência; com linkOnly (latência e perda), só no caminho do alvo até ela.
  dependency?: Dependency;
  linkOnly?: boolean;
};

export type Scenario = {
  id: string;
  name: string;
  target: Target;
  mode: Mode;
  endpoints: Endpoint[];
  profile: Profile;
  faults?: Fault[];
  probePath?: string;
  slo?: SLO;
  requestTimeoutSeconds?: number;
  code?: CodeRepo;
};

// Repositório da aplicação no GitHub, lido pela IA.
// testImage e testCommand dizem como construir e testar o repositório; só as correções automáticas usam.
export type CodeRepo = { repo: string; branch: string; path?: string; testImage?: string; testCommand?: string };

export type SLO = { p95Ms: number; errorRatePct: number; availabilityPct: number };

export const defaultSLO: SLO = { p95Ms: 500, errorRatePct: 1, availabilityPct: 99 };

export type RunStatus = 'pending' | 'running' | 'completed' | 'aborted' | 'failed';

export type Window = {
  t: number;
  // Nulos num segundo sem requisição concluída (lacuna no gráfico).
  p50: number | null;
  p95: number | null;
  p99: number | null;
  rps: number;
  errorRate: number | null;
  // Usuários ativos e nenhuma resposta: requisições presas (alvo fora do ar).
  stalled?: boolean;
  s2xx: number;
  s3xx: number;
  s4xx: number;
  s5xx: number;
  sErr: number;
  vus: number;
  cpu: number;
  memMb: number;
  probe?: Probe;
  // Kubernetes: pods do Deployment prontos e existentes.
  pods?: { ready: number; total: number };
};

export type Probe = { ms: number; status: number; ok: boolean; error?: string };

export type FaultState = 'active' | 'reverted' | 'failed' | 'skipped';

export type FaultEvent = {
  faultId: string;
  type: FaultType;
  state: FaultState;
  appliedAt?: number;
  revertedAt?: number;
  error?: string;
};

// Logs e eventos coletados durante a execução (o que a IA recebe, resumido).
export type LogLine = { t: number; text: string };
export type PodLog = {
  namespace?: string;
  pod: string;
  container?: string;
  role: string;
  previous?: boolean;
  gone?: boolean;
  truncated?: boolean;
  lines: LogLine[];
};
export type KubeEvent = { t: number; type: string; reason: string; namespace: string; object: string; message: string; count?: number };
export type MetricSeries = { service: string; key: string; label: string; unit: string; points: { t: number; v: number }[] };
export type SpanSummary = { service: string; name: string; durationMs: number; error?: boolean; detail?: string };
export type TraceSummary = { service: string; kind: string; traceId: string; t: number; root: string; durationMs: number; spans?: SpanSummary[] };
export type ProfileTop = { service: string; type: string; functions: { name: string; selfPct: number }[] };
export type AppTelemetry = {
  metrics: MetricSeries[];
  traces: TraceSummary[];
  profiles: ProfileTop[];
  available?: Record<string, string[]>;
  notes?: string[];
  grafanaUrl?: string;
};
export type Diagnostics = { logs: PodLog[]; events: KubeEvent[]; notes?: string[]; app?: AppTelemetry };

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

export type Grade = 'resiliente' | 'atencao' | 'fragil' | 'sem_dados';

export type Phase = { seconds: number; inSlo: number; compliancePct: number };

export type FaultResult = {
  faultId: string;
  type: FaultType;
  params?: Record<string, string>;
  state: FaultState;
  start: number;
  end: number;
  during: Phase;
  recovered: boolean;
  recoverySeconds?: number;
  worstP95Ms?: number;
  worstErrorRatePct?: number;
  stalledSeconds?: number;
  probeFailures?: number;
  score: number;
};

export type Verdict = {
  score: number;
  grade: Grade;
  slo: SLO;
  overall: Phase;
  baseline: Phase;
  faults: FaultResult[];
  checks: { name: string; target: string; actual: string; pass: boolean }[];
  notes?: string[];
};

// Correção sugerida. diff é o patch; applies diz se ele se aplica ao commit analisado (ausente = não conferido).
export type ReportFix = {
  title: string;
  detail: string;
  file?: string;
  line?: number;
  code?: string;
  url?: string;
  newFile?: boolean;
  diff?: string;
  metric?: string;
  expected?: string;
  applies?: boolean;
  applyError?: string;
};

export type ReportContent = {
  summary: string;
  scoreExplanation: string;
  breakingPoint: { found: boolean; description: string };
  faultFindings: { faultId: string; finding: string }[];
  causes: string[];
  fixes: ReportFix[];
  regression?: { verdict: 'melhorou' | 'igual' | 'piorou'; summary: string; items: string[] };
};

export type Report = {
  content: ReportContent;
  provider: string;
  usage: { model: string; inputTokens: number; outputTokens: number; cacheReadTokens?: number; toolCalls?: number };
  referenceRunId?: string;
  // pinned: o commit é o que estava implantado no teste (senão, o topo do branch na hora da análise).
  code?: { repo: string; branch: string; path?: string; commit: string; pinned?: boolean; filesRead: string[] };
  score: number;
  createdAt: string;
};

export type ChatMessage = { id: number; role: 'user' | 'assistant'; content: string; createdAt: string };

export type AIModel = { id: string; label: string; note?: string };

export type AIProvider = { id: string; label: string; keyEnv: string; configured: boolean; defaultModel: string; models: AIModel[] };

export type AISettings = { provider: string; model: string; providers?: AIProvider[]; githubTokenConfigured?: boolean };

// Versão de um componente testado (o alvo ou uma dependência) no início da execução.
export type ImageRef = { container?: string; image: string; id?: string; pods?: number };
export type Version = { role: 'target' | 'dependency'; name: string; commit?: string; images: ImageRef[]; mixed?: boolean };

// Números de um trecho da execução, calculados pelo k6 sobre todas as requisições do trecho.
export type Segment = {
  seconds: number;
  requests: number;
  rps: number;
  errorRatePct: number;
  avgMs: number;
  p50Ms: number;
  p90Ms: number;
  p95Ms: number;
  p99Ms: number;
  maxMs: number;
};
export type EndpointSummary = Segment & { method: string; path: string };

// Resumo da execução inteira: overall inclui o aquecimento; steady e endpoints, não.
export type Summary = {
  openModel: boolean;
  targetRate?: number;
  overall: Segment;
  warmupSeconds: number;
  steady: Segment;
  endpoints: EndpointSummary[];
  unmeasuredEndpoints?: number;
  droppedIterations?: number;
  warnings?: string[]; // por que a execução não serve para comparação
};

// --- correções automáticas ---

export type FixStatus = 'awaiting_approval' | 'queued' | 'running' | 'pr_open' | 'rejected' | 'failed';
export type PatchFile = { path: string; oldPath?: string; added: number; removed: number; new?: boolean; deleted?: boolean };
export type PatchReview = { files: PatchFile[]; lines: number; violations: string[]; highlights: string[] };
export type FixRound = {
  n: number;
  startedAt: string;
  endedAt: string;
  applied: boolean;
  agentRan: boolean;
  agentOutput?: string;
  agentError?: string;
  costUsd?: number;
  diff: string;
  review?: PatchReview;
  testExit: number;
  testLog?: string;
  error?: string;
};
// Tentativa de transformar uma correção sugerida num PR em rascunho.
export type FixAttempt = {
  id: string;
  runId: string;
  scenarioId: string;
  fixIndex: number;
  title: string;
  detail: string;
  metric?: string;
  expected?: string;
  repo: string;
  baseBranch: string;
  commit: string;
  path?: string;
  testImage: string;
  testCommand: string;
  model: string;
  proposed: string;
  review: PatchReview;
  status: FixStatus;
  error?: string;
  createdBy?: string;
  decidedBy?: string;
  createdAt: string;
  decidedAt?: string;
  endedAt?: string;
  rounds: FixRound[];
  finalDiff?: string;
  costUsd: number;
  pr?: { number: number; url: string; branch: string; commit: string };
  highlights?: string[];
  // Só no GET de uma tentativa: por que não pode ser aprovada agora e se quem está logado pode decidir.
  blocked?: string;
  canDecide?: boolean;
};

// --- experimentos A/B ---

export type ExperimentStatus = 'pending' | 'running' | 'completed' | 'aborted' | 'failed';
export type Outcome = 'melhorou' | 'piorou' | 'sem_diferenca' | 'inconclusivo';
export type MetricName = 'p50' | 'p95' | 'p99' | 'avg' | 'error_rate' | 'score' | 'cpu_per_request';
export type ExperimentMetric = { name: MetricName; endpoint?: number };

// Comparação de B com A numa métrica: medianas, diferença e intervalo de 95% (bootstrap).
export type Comparison = {
  nA: number;
  nB: number;
  medianA: number;
  medianB: number;
  diff: number;
  lo: number;
  hi: number;
  diffPct: number;
  loPct: number;
  hiPct: number;
  marginAbs: number;
  outcome: Outcome;
  why: string;
};

export type ArmRun = {
  arm: 'A' | 'B';
  runId?: string;
  status: string;
  valid: boolean;
  invalid?: string;
  values?: Partial<Record<MetricName, number>>;
  version?: string;
  commit?: string;
};

export type ExperimentResult = {
  outcome: Outcome;
  primary: Comparison;
  guards: (Comparison & { metric: MetricName })[];
  noisePct?: number;
  notes: string[];
};

// b ausente = teste A/A (a mesma versão dos dois lados, para medir o ruído do ambiente).
export type ExperimentSpec = {
  name?: string;
  scenarioId: string;
  b?: Target;
  metric: ExperimentMetric;
  minRuns?: number;
  maxRuns?: number;
  marginPct?: number;
  cooldownSeconds?: number;
};

export type Experiment = Required<Omit<ExperimentSpec, 'b'>> & {
  id: string;
  b?: Target;
  scenario: Scenario;
  status: ExperimentStatus;
  error?: string;
  createdAt: string;
  endedAt?: string;
  runs: ArmRun[];
  result?: ExperimentResult;
};

export type RunDetail = Run & {
  experiment?: { experimentId: string; arm: 'A' | 'B' };
  versions?: Version[];
  summary?: Summary;
  windows: Window[];
  faultEvents: FaultEvent[];
  verdict?: Verdict;
  referenceRunId?: string;
  analysis?: Report;
};

export type LiveEvent =
  | { type: 'windows'; windows: Window[] }
  | { type: 'status'; status: RunStatus; error?: string }
  | { type: 'fault'; fault: FaultEvent };

async function call<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  if (!res.ok) {
    if (res.status === 401 && !path.startsWith('/api/auth/')) window.dispatchEvent(new Event(UNAUTHORIZED_EVENT));
    const data = await res.json().catch(() => ({}));
    throw new Error(data.error ?? `HTTP ${res.status}`);
  }
  if (res.status === 204 || res.status === 202) return undefined as T;
  return res.json();
}

// Endereço do Grafana para uma execução: Explore com o p95 HTTP do serviço alvo, no intervalo
// da execução (com folga antes e depois, porque as métricas chegam a cada 5 s).
export function grafanaExploreUrl(base: string, job: string, from: number, to: number): string {
  const expr = `histogram_quantile(0.95, sum by (le) (rate(http_server_request_duration_seconds_bucket{job="${job}"}[30s])))`;
  const panes = {
    a: {
      datasource: 'prometheus',
      queries: [{ refId: 'A', expr, datasource: { type: 'prometheus', uid: 'prometheus' } }],
      range: { from: String(from), to: String(to) },
    },
  };
  return `${base.replace(/\/$/, '')}/explore?schemaVersion=1&orgId=1&panes=${encodeURIComponent(JSON.stringify(panes))}`;
}

export const api = {
  health: () => call<Health>('GET', '/api/health'),
  me: () => call<Me>('GET', '/api/auth/me'),
  login: (username: string, password: string) => call<Me>('POST', '/api/auth/login', { username, password }),
  logout: () => call<void>('POST', '/api/auth/logout'),
  users: () => call<User[]>('GET', '/api/users'),
  createUser: (username: string, password: string, admin: boolean) => call<User>('POST', '/api/users', { username, password, admin }),
  deleteUser: (username: string) => call<void>('DELETE', `/api/users/${encodeURIComponent(username)}`),
  containers: () => call<Container[]>('GET', '/api/containers'),
  deployments: () => call<DeploymentInfo[]>('GET', '/api/deployments'),
  scenarios: () => call<Scenario[]>('GET', '/api/scenarios'),
  scenario: (id: string) => call<Scenario>('GET', `/api/scenarios/${id}`),
  createScenario: (s: Omit<Scenario, 'id'>) => call<Scenario>('POST', '/api/scenarios', s),
  updateScenario: (s: Scenario) => call<Scenario>('PUT', `/api/scenarios/${s.id}`, s),
  deleteScenario: (id: string) => call<void>('DELETE', `/api/scenarios/${id}`),
  startRun: (scenarioId: string) => call<Run>('POST', `/api/scenarios/${scenarioId}/runs`),
  runs: (scenarioId?: string) => call<Run[]>('GET', `/api/runs${scenarioId ? `?scenarioId=${scenarioId}` : ''}`),
  run: (id: string) => call<RunDetail>('GET', `/api/runs/${id}`),
  diagnostics: (id: string) => call<Diagnostics>('GET', `/api/runs/${id}/diagnostics`),
  abortRun: (id: string) => call<void>('POST', `/api/runs/${id}/abort`),
  setReference: (scenarioId: string, runId: string) => call<{ referenceRunId: string }>('PUT', `/api/scenarios/${scenarioId}/reference`, { runId }),
  analyze: (runId: string) => call<Report>('POST', `/api/runs/${runId}/analysis`),
  chat: (runId: string) => call<ChatMessage[]>('GET', `/api/runs/${runId}/chat`),
  ask: (runId: string, message: string) => call<ChatMessage[]>('POST', `/api/runs/${runId}/chat`, { message }),
  clearChat: (runId: string) => call<void>('DELETE', `/api/runs/${runId}/chat`),
  fixes: (runId?: string) => call<FixAttempt[]>('GET', `/api/fixes${runId ? `?runId=${runId}` : ''}`),
  fix: (id: string) => call<FixAttempt>('GET', `/api/fixes/${id}`),
  createFix: (runId: string, index: number) => call<FixAttempt>('POST', `/api/runs/${runId}/fixes`, { index }),
  approveFix: (id: string) => call<FixAttempt>('POST', `/api/fixes/${id}/approve`),
  rejectFix: (id: string) => call<FixAttempt>('POST', `/api/fixes/${id}/reject`),
  experiments: (scenarioId?: string) => call<Experiment[]>('GET', `/api/experiments${scenarioId ? `?scenarioId=${scenarioId}` : ''}`),
  experiment: (id: string) => call<Experiment>('GET', `/api/experiments/${id}`),
  createExperiment: (s: ExperimentSpec) => call<Experiment>('POST', '/api/experiments', s),
  abortExperiment: (id: string) => call<void>('POST', `/api/experiments/${id}/abort`),
  aiSettings: () => call<AISettings>('GET', '/api/settings/ai'),
  saveAISettings: (s: AISettings) => call<AISettings>('PUT', '/api/settings/ai', { provider: s.provider, model: s.model }),
};

// Mesma conversão de perfil em stages do backend (internal/scenario), para a prévia no editor.
export function profileStages(p: Profile): { durationSeconds: number; target: number }[] {
  const d = p.durationSeconds;
  const n = openModel(p) ? p.rate! : p.vus;
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
