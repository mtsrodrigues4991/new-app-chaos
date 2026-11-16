import { ReactNode, useEffect, useState } from 'react';
import {
  api,
  ArmRun,
  Comparison,
  Container,
  DeploymentInfo,
  Experiment,
  ExperimentStatus,
  Health,
  MetricName,
  openModel,
  Outcome,
  Scenario,
  Target,
  targetName,
  targetPlatform,
} from '../api';
import { go, query } from '../router';

const statusLabel: Record<ExperimentStatus, string> = {
  pending: 'Preparando',
  running: 'Rodando',
  completed: 'Concluído',
  aborted: 'Abortado',
  failed: 'Falhou',
};

const outcomeLabel: Record<Outcome, string> = {
  melhorou: 'B melhorou',
  piorou: 'B piorou',
  sem_diferenca: 'Sem diferença',
  inconclusivo: 'Inconclusivo',
};

// Classe de cor reaproveitada do chip da nota.
const outcomeClass: Record<Outcome, string> = {
  melhorou: 'resiliente',
  piorou: 'fragil',
  sem_diferenca: '',
  inconclusivo: 'atencao',
};

const metricLabel: Record<MetricName, string> = {
  p50: 'p50 (ms)',
  p95: 'p95 (ms)',
  p99: 'p99 (ms)',
  avg: 'latência média (ms)',
  error_rate: 'taxa de erro (%)',
  score: 'nota de resiliência',
  cpu_per_request: 'CPU por requisição',
};

const n = (v: number, d = 2) => v.toLocaleString('pt-BR', { maximumFractionDigits: d, minimumFractionDigits: d });
const signed = (v: number, d = 2) => `${v > 0 ? '+' : ''}${n(v, d)}`;

function OutcomeChip({ outcome }: { outcome: Outcome }) {
  return <span className={`grade-chip ${outcomeClass[outcome]}`}>{outcomeLabel[outcome]}</span>;
}

function metricName(e: Experiment): string {
  const ep = e.metric.endpoint;
  const where = ep === undefined || ep === null ? '' : ` de ${e.scenario.endpoints[ep]?.method} ${e.scenario.endpoints[ep]?.path}`;
  return metricLabel[e.metric.name] + where;
}

function kind(e: Experiment): string {
  return e.b ? `A/B: ${targetName(e.scenario.target)} contra ${targetName(e.b)}` : `A/A: ${targetName(e.scenario.target)} dos dois lados`;
}

export default function Experiments() {
  const scenarioId = query('cenario') ?? undefined;
  const [items, setItems] = useState<Experiment[]>([]);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const load = () =>
      api
        .experiments(scenarioId)
        .then(setItems)
        .catch((e: Error) => setError(e.message));
    load();
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [scenarioId]);
  return (
    <section>
      <div className="title-row">
        <h1>Experimentos</h1>
        <a className="button primary" href="#/experimentos/novo">
          Novo experimento
        </a>
      </div>
      <p className="muted">
        Um experimento roda o mesmo cenário contra duas versões, várias vezes e intercalado, e decide com estatística se a versão B melhorou,
        piorou ou não difere. A decisão não usa IA.
      </p>
      {error && <p className="error">Erro: {error}</p>}
      {items.length === 0 && !error && <p className="muted">Nenhum experimento ainda. Comece por um teste A/A do cenário, para medir o ruído do ambiente.</p>}
      {items.length > 0 && (
        <div className="card table-card">
          <table>
            <thead>
              <tr>
                <th>Início</th>
                <th>Cenário</th>
                <th>Comparação</th>
                <th>Métrica</th>
                <th>Estado</th>
                <th>Resultado</th>
              </tr>
            </thead>
            <tbody>
              {items.map((e) => (
                <tr key={e.id}>
                  <td>
                    <a href={`#/experimentos/${e.id}`}>{new Date(e.createdAt).toLocaleString('pt-BR')}</a>
                  </td>
                  <td>{e.scenario.name}</td>
                  <td>{kind(e)}</td>
                  <td>{metricName(e)}</td>
                  <td>
                    <span className={`badge ${e.status}`}>{statusLabel[e.status]}</span>
                  </td>
                  <td>{e.result && e.status === 'completed' ? <OutcomeChip outcome={e.result.outcome} /> : '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

export function NewExperiment() {
  const [scenarios, setScenarios] = useState<Scenario[]>([]);
  const [health, setHealth] = useState<Health | null>(null);
  const [containers, setContainers] = useState<Container[]>([]);
  const [deployments, setDeployments] = useState<DeploymentInfo[]>([]);
  const [scenarioId, setScenarioId] = useState(query('cenario') ?? '');
  const [aa, setAA] = useState(true);
  const [b, setB] = useState<Target>({ baseUrl: '' });
  const [metric, setMetric] = useState<MetricName>('p95');
  const [endpoint, setEndpoint] = useState('');
  const [minRuns, setMinRuns] = useState(5);
  const [maxRuns, setMaxRuns] = useState(10);
  const [marginPct, setMarginPct] = useState(5);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    api.scenarios().then(setScenarios).catch((e: Error) => setError(e.message));
    api
      .health()
      .then((h) => {
        setHealth(h);
        if (h.docker) api.containers().then(setContainers).catch(() => undefined);
        if (h.kubernetes) api.deployments().then(setDeployments).catch(() => undefined);
      })
      .catch(() => undefined);
  }, []);

  const sc = scenarios.find((s) => s.id === scenarioId);
  const k8s = sc ? targetPlatform(sc.target) === 'kubernetes' : !!health?.kubernetes;
  const mode = sc?.mode || 'load';
  const metrics: MetricName[] = mode === 'chaos' ? ['score'] : mode === 'both' ? ['p95', 'p99', 'p50', 'avg', 'error_rate', 'score'] : ['p95', 'p99', 'p50', 'avg', 'error_rate'];
  const closed = sc && mode !== 'chaos' && !openModel(sc.profile);
  const perRun = sc ? sc.profile.durationSeconds + 15 : 0;

  const pickScenario = (id: string) => {
    setScenarioId(id);
    const s = scenarios.find((x) => x.id === id);
    if (s && (s.mode || 'load') === 'chaos') setMetric('score');
    else if (metric === 'score' && s && (s.mode || 'load') === 'load') setMetric('p95');
    setEndpoint('');
  };

  const submit = async () => {
    if (!sc) return;
    setBusy(true);
    setError(null);
    try {
      const e = await api.createExperiment({
        scenarioId,
        b: aa ? undefined : { ...b, platform: sc.target.platform },
        metric: { name: metric, endpoint: endpoint === '' || metric === 'score' ? undefined : Number(endpoint) },
        minRuns,
        maxRuns,
        marginPct,
      });
      go(`/experimentos/${e.id}`);
    } catch (err) {
      setError((err as Error).message);
      setBusy(false);
    }
  };

  return (
    <section className="editor">
      <div className="crumbs">
        <nav aria-label="Trilha">
          <a href="#/experimentos">Experimentos</a>
          <span aria-hidden="true">/</span>
          <span className="current">Novo</span>
        </nav>
      </div>
      <h1>Novo experimento</h1>
      <fieldset>
        <legend>Cenário</legend>
        <label>
          O teste que vai rodar nos dois lados
          <select value={scenarioId} onChange={(e) => pickScenario(e.target.value)}>
            <option value="">Escolha…</option>
            {scenarios.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name} ({targetName(s.target)})
              </option>
            ))}
          </select>
        </label>
        {closed && (
          <p className="notice">
            Este cenário usa carga em usuários virtuais: a versão mais rápida receberia mais carga e as latências não seriam comparáveis.{' '}
            <a href={`#/cenarios/${sc.id}`}>Mude o perfil para taxa de chegada</a> antes de criar o experimento.
          </p>
        )}
      </fieldset>

      <fieldset>
        <legend>Versões</legend>
        <p className="muted small">
          A é o alvo do cenário{sc ? ` (${targetName(sc.target)})` : ''}. Você implanta a versão B como outro alvo; o experimento só roda e compara.
        </p>
        <div className="profile-types">
          <label className={`choice ${aa ? 'selected' : ''}`}>
            <input type="radio" name="kind" checked={aa} onChange={() => setAA(true)} />
            <strong>Teste A/A</strong>
            <span className="muted">A mesma versão dos dois lados. Mede o ruído do ambiente; faça antes do primeiro A/B do cenário.</span>
          </label>
          <label className={`choice ${!aa ? 'selected' : ''}`}>
            <input type="radio" name="kind" checked={!aa} onChange={() => setAA(false)} />
            <strong>A/B</strong>
            <span className="muted">Compara o alvo do cenário (A) com outro alvo que roda a versão candidata (B).</span>
          </label>
        </div>
        {!aa && (
          <div className="row">
            {k8s ? (
              <label>
                Deployment da versão B
                <select
                  value={b.namespace && b.deployment ? `${b.namespace}/${b.deployment}` : ''}
                  onChange={(e) => {
                    const d = deployments.find((x) => `${x.namespace}/${x.name}` === e.target.value);
                    setB(d ? { namespace: d.namespace, deployment: d.name, baseUrl: d.urls[0] ?? '' } : { baseUrl: '' });
                  }}
                >
                  <option value="">Escolha…</option>
                  {deployments.map((d) => (
                    <option key={`${d.namespace}/${d.name}`} value={`${d.namespace}/${d.name}`}>
                      {d.namespace}/{d.name}
                    </option>
                  ))}
                </select>
              </label>
            ) : (
              <label>
                Container da versão B
                <select
                  value={b.container ?? ''}
                  onChange={(e) => {
                    const c = containers.find((x) => x.name === e.target.value);
                    setB(c ? { container: c.name, baseUrl: `http://${c.service || c.name}${c.ports[0] ? `:${c.ports[0]}` : ''}` } : { baseUrl: '' });
                  }}
                >
                  <option value="">Escolha…</option>
                  {containers.map((c) => (
                    <option key={c.id} value={c.name}>
                      {c.name}
                    </option>
                  ))}
                </select>
              </label>
            )}
            <label>
              URL base da versão B
              <input value={b.baseUrl} onChange={(e) => setB({ ...b, baseUrl: e.target.value })} placeholder="http://demo-api-b:8080" />
            </label>
          </div>
        )}
      </fieldset>

      <fieldset>
        <legend>Métrica e regra</legend>
        <p className="muted small">
          A métrica principal é escolhida agora, antes de rodar: olhar várias depois e ficar com a que melhorou não vale. Taxa de erro, nota e
          CPU por requisição entram como guardas: se uma delas piorar, o resultado é "piorou".
        </p>
        <div className="row">
          <label>
            Métrica principal
            <select value={metric} onChange={(e) => setMetric(e.target.value as MetricName)}>
              {metrics.map((m) => (
                <option key={m} value={m}>
                  {metricLabel[m]}
                </option>
              ))}
            </select>
          </label>
          {metric !== 'score' && sc && (
            <label>
              Endpoint
              <select value={endpoint} onChange={(e) => setEndpoint(e.target.value)}>
                <option value="">Todos juntos</option>
                {sc.endpoints.slice(0, 20).map((ep, i) => (
                  <option key={i} value={i}>
                    {ep.method} {ep.path}
                  </option>
                ))}
              </select>
            </label>
          )}
          <label>
            Limite prático (%)
            <input type="number" min={0.5} max={50} step={0.5} value={marginPct} onChange={(e) => setMarginPct(Number(e.target.value))} />
          </label>
        </div>
        <div className="row">
          <label>
            Execuções por versão (mínimo)
            <input type="number" min={5} max={30} value={minRuns} onChange={(e) => setMinRuns(Number(e.target.value))} />
          </label>
          <label>
            Execuções por versão (teto)
            <input type="number" min={minRuns} max={30} value={maxRuns} onChange={(e) => setMaxRuns(Number(e.target.value))} />
          </label>
        </div>
        {sc && (
          <p className="muted small">
            Tempo estimado: de {Math.ceil((2 * minRuns * perRun) / 60)} a {Math.ceil((2 * maxRuns * perRun) / 60)} minutos. O experimento para assim
            que a decisão deixa de ser "inconclusivo". Nenhuma outra execução roda enquanto isso.
          </p>
        )}
      </fieldset>
      {error && <p className="error">Erro: {error}</p>}
      <button className="primary" disabled={!sc || busy || !!closed || (!aa && !b.baseUrl)} onClick={submit}>
        {busy ? 'Iniciando…' : 'Rodar experimento'}
      </button>
    </section>
  );
}

function ComparisonRow({ label, c, unitDigits = 2 }: { label: ReactNode; c: Comparison; unitDigits?: number }) {
  return (
    <tr>
      <td>{label}</td>
      <td style={{ textAlign: 'right' }}>{n(c.medianA, unitDigits)}</td>
      <td style={{ textAlign: 'right' }}>{n(c.medianB, unitDigits)}</td>
      <td style={{ textAlign: 'right' }}>
        {signed(c.diff, unitDigits)} ({signed(c.diffPct, 1)}%)
      </td>
      <td style={{ textAlign: 'right' }}>
        {signed(c.lo, unitDigits)} a {signed(c.hi, unitDigits)}
      </td>
      <td style={{ textAlign: 'right' }}>
        {c.nA} e {c.nB}
      </td>
      <td>
        <OutcomeChip outcome={c.outcome} />
      </td>
    </tr>
  );
}

export function ExperimentView({ id }: { id: string }) {
  const [e, setE] = useState<Experiment | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    let stop = false;
    const load = () =>
      api
        .experiment(id)
        .then((x) => {
          if (!stop) setE(x);
        })
        .catch((err: Error) => setError(err.message));
    load();
    const t = setInterval(load, 3000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [id]);
  if (error) return <p className="error">Erro: {error}</p>;
  if (!e) return null;
  const done = e.status !== 'pending' && e.status !== 'running';
  const valid = (arm: 'A' | 'B') => e.runs.filter((r) => r.arm === arm && r.valid).length;
  const r = e.result;
  const m = e.metric.name;
  const value = (run: ArmRun) => (run.values?.[m] === undefined ? '—' : n(run.values[m]!));
  return (
    <section>
      <div className="crumbs">
        <nav aria-label="Trilha">
          <a href="#/experimentos">Experimentos</a>
          <span aria-hidden="true">/</span>
          <span className="current">{e.name}</span>
          <span className="mono">#{e.id.slice(0, 8)}</span>
        </nav>
        {!done && (
          <button className="danger" onClick={() => api.abortExperiment(e.id).catch((err: Error) => setError(err.message))}>
            Abortar
          </button>
        )}
      </div>
      <div className="card">
        <div className="card-head">
          <h2>{kind(e)}</h2>
          <span className={`badge ${e.status}`}>{statusLabel[e.status]}</span>
        </div>
        <p className="muted">
          Cenário <a href={`#/cenarios/${e.scenarioId}`}>{e.scenario.name}</a> · métrica principal: {metricName(e)} · limite prático de {n(e.marginPct, 1)}% ·{' '}
          {e.minRuns} a {e.maxRuns} execuções por versão
        </p>
        {!done && (
          <p>
            Rodando: {valid('A')} execuções válidas de A e {valid('B')} de B (mínimo de {e.minRuns} de cada).
          </p>
        )}
        {e.error && <p className="error">{e.error}</p>}
        {done && r && (
          <>
            <p style={{ fontSize: 20, margin: '12px 0' }}>
              <OutcomeChip outcome={r.outcome} />
            </p>
            {r.noisePct !== undefined && r.noisePct !== null && (
              <p className="muted small">
                Piso de ruído {e.b ? 'do último teste A/A deste cenário' : 'medido aqui'}: {n(r.noisePct, 1)}% na métrica principal.
              </p>
            )}
            <ul className="small">
              {r.notes.map((note, i) => (
                <li key={i}>{note}</li>
              ))}
            </ul>
          </>
        )}
      </div>
      {r && (
        <div className="card table-card">
          <div className="card-head" style={{ padding: '16px 20px 0' }}>
            <h2>Comparação</h2>
            <span className="muted small">medianas das execuções válidas; intervalo de 95% da diferença (B − A)</span>
          </div>
          <table>
            <thead>
              <tr>
                <th>Métrica</th>
                <th style={{ textAlign: 'right' }}>A</th>
                <th style={{ textAlign: 'right' }}>B</th>
                <th style={{ textAlign: 'right' }}>Diferença</th>
                <th style={{ textAlign: 'right' }}>Intervalo de 95%</th>
                <th style={{ textAlign: 'right' }}>Execuções</th>
                <th>Decisão</th>
              </tr>
            </thead>
            <tbody>
              <ComparisonRow label={<strong>{metricName(e)}</strong>} c={r.primary} />
              {r.guards.map((g) => (
                <ComparisonRow key={g.metric} label={`guarda: ${metricLabel[g.metric]}`} c={g} unitDigits={g.metric === 'cpu_per_request' ? 4 : 2} />
              ))}
            </tbody>
          </table>
        </div>
      )}
      <div className="card table-card">
        <div className="card-head" style={{ padding: '16px 20px 0' }}>
          <h2>Execuções</h2>
          <span className="muted small">na ordem em que rodaram</span>
        </div>
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Lado</th>
              <th>Execução</th>
              <th style={{ textAlign: 'right' }}>{metricLabel[m]}</th>
              <th>Vale?</th>
              <th>Versão</th>
            </tr>
          </thead>
          <tbody>
            {e.runs.map((run, i) => (
              <tr key={i}>
                <td>{i + 1}</td>
                <td>{run.arm}</td>
                <td>{run.runId ? <a href={`#/execucoes/${run.runId}`}>#{run.runId.slice(0, 8)}</a> : '—'}</td>
                <td style={{ textAlign: 'right' }}>{value(run)}</td>
                <td>{run.valid ? 'sim' : <span className="error">não: {run.invalid}</span>}</td>
                <td className="mono small" style={{ overflowWrap: 'anywhere' }}>
                  {run.commit ? `${run.commit.slice(0, 12)} · ` : ''}
                  {run.version ? run.version.replace(/sha256:([0-9a-f]{12})[0-9a-f]+/g, 'sha256:$1…') : 'não registrada'}
                </td>
              </tr>
            ))}
            {e.runs.length === 0 && (
              <tr>
                <td colSpan={6} className="muted">
                  A primeira execução ainda está rodando.
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </section>
  );
}
