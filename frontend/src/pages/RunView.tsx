import { ReactNode, useEffect, useMemo, useState } from 'react';
import { AppView, hasLogs, LogsAndEvents, useDiagnostics } from '../components/Diagnostics';
import Icon from '../components/Icon';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ReferenceArea,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { api, Fault, FaultEvent, FaultResult, FaultState, Grade, LiveEvent, Run, RunDetail, RunStatus, targetName, targetPlatform, Verdict, Window } from '../api';
import AIPanel from '../components/AIPanel';
import { faultMeta, faultSummary } from '../faults';
import { modeLabel, profileLabel } from './ScenarioEditor';

const statusLabel: Record<RunStatus, string> = {
  pending: 'Preparando',
  running: 'Rodando',
  completed: 'Concluída',
  aborted: 'Abortada',
  failed: 'Falhou',
};

export function StatusBadge({ status }: { status: RunStatus }) {
  return (
    <span className={`badge ${status}`}>
      {statusLabel[status]}
    </span>
  );
}

export function duration(r: Run): string {
  if (!r.startedAt) return '—';
  const end = r.endedAt ? new Date(r.endedAt) : new Date();
  const s = Math.round((end.getTime() - new Date(r.startedAt).getTime()) / 1000);
  return s >= 60 ? `${Math.floor(s / 60)} min ${s % 60} s` : `${s} s`;
}

// Os gráficos se ajustam à largura da tela; antes de imprimir, a página encolhe para a largura
// do A4 e espera os gráficos se redesenharem, senão eles sairiam cortados no papel.
function printReport() {
  document.body.classList.add('print-mode');
  window.dispatchEvent(new Event('resize'));
  const done = () => {
    document.body.classList.remove('print-mode');
    window.removeEventListener('afterprint', done);
    window.dispatchEvent(new Event('resize'));
  };
  window.addEventListener('afterprint', done);
  setTimeout(() => window.print(), 400);
}

const live = (s: RunStatus) => s === 'pending' || s === 'running';

function mergeWindows(prev: Window[], next: Window[]): Window[] {
  const byT = new Map(prev.map((w) => [w.t, w]));
  for (const w of next) byT.set(w.t, w);
  return [...byT.values()].sort((a, b) => a.t - b.t);
}

function mergeFault(prev: FaultEvent[], ev: FaultEvent): FaultEvent[] {
  return [...prev.filter((f) => f.faultId !== ev.faultId), ev];
}

// Faixa de cada falha nos gráficos: instantes reais quando já aplicada, senão o planejado.
type Band = { fault: Fault; x1: number; x2: number; ev: FaultEvent | undefined };

function bands(run: RunDetail): Band[] {
  if (run.scenario.mode === 'load') return [];
  return (run.scenario.faults ?? [])
    .map((f) => {
      const ev = run.faultEvents.find((e) => e.faultId === f.id);
      if (ev?.state === 'skipped') return null;
      const x1 = ev?.appliedAt ?? f.startSeconds;
      // Ativa: mostra até o fim planejado (a reversão real chega com o evento seguinte).
      const x2 = ev?.revertedAt ?? Math.max(x1 + 0.5, f.startSeconds + f.durationSeconds);
      return { fault: f, x1, x2, ev };
    })
    .filter((b): b is Band => b !== null);
}

type Tab = 'resumo' | 'graficos' | 'telemetria' | 'logs' | 'ia';

export default function RunView({ id }: { id: string }) {
  const [run, setRun] = useState<RunDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [aborting, setAborting] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);
  const [tab, setTab] = useState<Tab | null>(null);

  useEffect(() => {
    let es: EventSource | null = null;
    let closed = false;
    api
      .run(id)
      .then((r) => {
        if (closed) return;
        setRun(r);
        if (!live(r.status)) return;
        es = new EventSource(`/api/runs/${id}/stream`);
        es.onmessage = (msg) => {
          const ev = JSON.parse(msg.data) as LiveEvent;
          setRun((cur) => {
            if (!cur) return cur;
            if (ev.type === 'windows') return { ...cur, windows: mergeWindows(cur.windows, ev.windows) };
            if (ev.type === 'fault') return { ...cur, faultEvents: mergeFault(cur.faultEvents ?? [], ev.fault) };
            const startedAt = cur.startedAt ?? (ev.status === 'running' ? new Date().toISOString() : undefined);
            return { ...cur, status: ev.status, error: ev.error, startedAt };
          });
        };
        es.addEventListener('end', () => {
          es?.close();
          // recarrega do banco para pegar horários finais e qualquer janela perdida
          api.run(id).then((r2) => !closed && setRun(r2));
        });
      })
      .catch((e: Error) => setError(e.message));
    return () => {
      closed = true;
      es?.close();
    };
  }, [id]);

  const finished = !!run && !live(run.status);
  const { diag, error: diagError } = useDiagnostics(id, finished);

  if (error) return <p className="error">Erro: {error}</p>;
  if (!run) return <p className="muted">Carregando…</p>;

  const abort = async () => {
    setAborting(true);
    setActionError(null);
    try {
      await api.abortRun(id);
    } catch (e) {
      // Se a execução já terminou nesse meio tempo, basta recarregar o estado.
      const fresh = await api.run(id).catch(() => null);
      if (fresh) setRun(fresh);
      if (!fresh || live(fresh.status)) setActionError((e as Error).message);
      setAborting(false);
    }
  };

  const p = run.scenario.profile;
  const mode = run.scenario.mode || 'load';
  const bs = bands(run);
  const spans = bs.filter((b) => b.ev?.appliedAt !== undefined).map((b) => ({ label: faultMeta[b.fault.type].short, from: b.x1, to: b.x2 }));
  const warnings = diag ? diag.events.filter((e) => e.type === 'Warning').length : 0;
  const tabs: { id: Tab; label: string; count?: number; bad?: boolean }[] = [
    { id: 'resumo', label: 'Resumo' },
    { id: 'graficos', label: 'Gráficos' },
    ...(diag?.app ? [{ id: 'telemetria' as Tab, label: 'Telemetria da app' }] : []),
    ...(diag && hasLogs(diag) ? [{ id: 'logs' as Tab, label: 'Logs e eventos', count: warnings || undefined, bad: warnings > 0 }] : []),
    ...(run.verdict && run.windows.length > 0 ? [{ id: 'ia' as Tab, label: 'Análise da IA' }] : []),
  ];
  // Enquanto roda, o que interessa são os gráficos ao vivo; depois, o resumo.
  const current: Tab = tab && tabs.some((t) => t.id === tab) ? tab : finished ? 'resumo' : 'graficos';
  const facts = [
    modeLabel[mode],
    mode === 'chaos' ? `sonda ${run.scenario.probePath}` : `${profileLabel[p.type]}, ${p.vus} VUs`,
    `${p.durationSeconds} s planejados`,
    `iniciada em ${new Date(run.createdAt).toLocaleString('pt-BR')}`,
    `duração ${duration(run)}`,
  ];
  return (
    <section className="run-view">
      <p className="print-only small muted">
        new-app-chaos · relatório da execução {run.id} · impresso em {new Date().toLocaleString('pt-BR')}
      </p>
      <div className="crumbs">
        <nav aria-label="Trilha">
          <a href="#/execucoes">Execuções</a>
          <span aria-hidden="true">/</span>
          <span className="current">{run.scenarioName}</span>
          <span className="mono">#{run.id.slice(0, 8)}</span>
        </nav>
        <div className="toolbar">
          <a className="button" href={`#/cenarios/${run.scenarioId}`}>
            <Icon name="edit" size={16} /> Editar cenário
          </a>
          {finished && (
            <button onClick={printReport}>
              <Icon name="printer" size={16} /> Imprimir / PDF
            </button>
          )}
          {live(run.status) && (
            <button className="danger" disabled={aborting} onClick={abort}>
              <Icon name="stop" size={16} /> {aborting ? 'Abortando…' : 'Abortar'}
            </button>
          )}
        </div>
      </div>
      <div className="hero">
        <div className="card hero-main">
          {run.verdict && <ScoreRing verdict={run.verdict} />}
          <div className="meta">
            <div className="actions">
              {run.verdict && run.verdict.grade !== 'sem_dados' && (
                <span className={`grade-chip ${run.verdict.grade}`}>
                  <Icon name={run.verdict.grade === 'resiliente' ? 'check' : 'alert'} size={13} />
                  {gradeLabel[run.verdict.grade]}
                </span>
              )}
              <StatusBadge status={run.status} />
            </div>
            <h1>{run.scenarioName}</h1>
            <div className="hero-facts">
              <span>
                Alvo <span className="mono">{targetName(run.scenario.target)}</span>
              </span>
              <span className="mono">{run.scenario.target.baseUrl}</span>
            </div>
            <div className="hero-facts">{facts.join(' · ')}</div>
          </div>
        </div>
        {mode === 'chaos' ? <ProbeSummary windows={run.windows} /> : <Summary windows={run.windows} />}
      </div>
      {run.status === 'pending' && mode !== 'chaos' && <p className="muted">Preparando o k6 (na primeira vez ele baixa a imagem, pode levar um minuto)…</p>}
      {run.error && <p className="error">{run.error}</p>}
      {actionError && <p className="error">Não foi possível abortar: {actionError}</p>}
      {diagError && <p className="error no-print">Logs e eventos: {diagError}</p>}
      <div className="tabs" role="tablist" aria-label="Seções da execução">
        {tabs.map((t) => (
          <button key={t.id} role="tab" id={`tab-${t.id}`} aria-selected={current === t.id} aria-controls={`panel-${t.id}`} onClick={() => setTab(t.id)}>
            {t.label}
            {t.count !== undefined && <span className={`tab-count ${t.bad ? 'bad' : ''}`}>{t.count}</span>}
          </button>
        ))}
      </div>
      <Panel id="resumo" current={current}>
        {run.verdict && <VerdictPanel run={run} onReference={(referenceRunId) => setRun({ ...run, referenceRunId })} onAI={() => setTab('ia')} />}
        {bs.length > 0 && <FaultList bands={bs} verdict={run.verdict} />}
        {!run.verdict && bs.length === 0 && <p className="muted">{finished ? 'Esta execução não tem veredito nem falhas.' : 'O veredito aparece quando a execução terminar.'}</p>}
      </Panel>
      <Panel id="graficos" current={current}>
        <Charts windows={run.windows} bands={bs} chaosOnly={mode === 'chaos'} duration={p.durationSeconds} k8s={targetPlatform(run.scenario.target) === 'kubernetes'} />
      </Panel>
      {diag?.app && (
        <Panel id="telemetria" current={current}>
          <AppView app={diag.app} spans={spans} />
        </Panel>
      )}
      {diag && hasLogs(diag) && (
        <Panel id="logs" current={current}>
          <LogsAndEvents diag={diag} spans={spans} />
        </Panel>
      )}
      {run.verdict && run.windows.length > 0 && (
        <Panel id="ia" current={current}>
          <AIPanel run={run} onReport={(analysis) => setRun({ ...run, analysis })} />
        </Panel>
      )}
    </section>
  );
}

// As abas escondem o conteúdo na tela, mas a impressão mostra todas (ver styles.css).
function Panel({ id, current, children }: { id: Tab; current: Tab; children: ReactNode }) {
  return (
    <div className="tab-panel" role="tabpanel" id={`panel-${id}`} aria-labelledby={`tab-${id}`} hidden={current !== id}>
      {children}
    </div>
  );
}

function ScoreRing({ verdict: v }: { verdict: Verdict }) {
  const r = 50;
  const c = 2 * Math.PI * r;
  const score = v.grade === 'sem_dados' ? 0 : v.score;
  return (
    <div className={`ring ${v.grade}`}>
      <svg width="120" height="120" viewBox="0 0 120 120" role="img" aria-label={v.grade === 'sem_dados' ? 'Sem nota' : `Nota ${v.score} de 100`}>
        <circle className="track" cx="60" cy="60" r={r} />
        <circle className="value" cx="60" cy="60" r={r} transform="rotate(-90 60 60)" strokeDasharray={`${(score / 100) * c} ${c}`} />
      </svg>
      <div className="ring-label" aria-hidden="true">
        <div className="score-value">{v.grade === 'sem_dados' ? '—' : v.score}</div>
        <small>de 100</small>
      </div>
    </div>
  );
}

const n = (v: number, d = 0) => v.toLocaleString('pt-BR', { maximumFractionDigits: d, minimumFractionDigits: d });

function Summary({ windows }: { windows: Window[] }) {
  const s = useMemo(() => {
    const active = windows.filter((w) => w.rps > 0);
    const reqs = windows.reduce((a, w) => a + w.rps, 0);
    const failed = windows.reduce((a, w) => a + (w.errorRate ?? 0) * w.rps, 0);
    const p95s = active.flatMap((w) => (w.p95 === null ? [] : [w.p95])).sort((a, b) => a - b);
    return {
      reqs,
      rps: active.length ? reqs / active.length : 0,
      p95: p95s.length ? p95s[Math.floor(p95s.length / 2)] : 0,
      p95max: p95s.length ? p95s[p95s.length - 1] : 0,
      errRate: reqs ? failed / reqs : 0,
      cpuMax: Math.max(0, ...windows.map((w) => w.cpu)),
      memMax: Math.max(0, ...windows.map((w) => w.memMb)),
      stalled: windows.filter((w) => w.stalled).length,
    };
  }, [windows]);
  const series = (f: (w: Window) => number | null) => windows.map((w) => f(w));
  return (
    <div className="tiles">
      <Tile label="Requisições" value={n(s.reqs)} hint={`${n(s.rps, 1)} por segundo em média`} spark={series((w) => w.rps)} />
      <Tile label="p95 típico" value={`${n(s.p95, 1)} ms`} hint={`pior janela: ${n(s.p95max, 1)} ms`} spark={series((w) => w.p95)} />
      <Tile label="Taxa de erro" value={`${n(s.errRate * 100, 2)}%`} bad={s.errRate > 0.01} spark={series((w) => w.errorRate)} />
      {s.stalled > 0 && <Tile label="Sem resposta" value={`${s.stalled} s`} hint="usuários ativos e nada respondeu" bad />}
      <Tile label="CPU do alvo (pico)" value={`${n(s.cpuMax, 1)}%`} hint="100% = 1 núcleo" spark={series((w) => w.cpu)} />
      <Tile label="Memória do alvo (pico)" value={`${n(s.memMax, 1)} MB`} spark={series((w) => w.memMb)} />
    </div>
  );
}

const faultStateLabel: Record<FaultState, string> = {
  active: '● ativa agora',
  reverted: 'revertida',
  failed: 'falhou',
  skipped: 'não executada',
};

const secs = (v?: number) => (v === undefined ? '—' : `${v.toLocaleString('pt-BR', { maximumFractionDigits: 1 })} s`);

function FaultList({ bands, verdict }: { bands: Band[]; verdict?: Verdict }) {
  const result = (id: string) => verdict?.faults.find((r) => r.faultId === id);
  const ok = verdict?.faults.filter((r) => r.score >= 90).length;
  return (
    <div className="card table-card">
      <div className="card-head" style={{ padding: '16px 20px 0' }}>
        <h2>Falhas</h2>
        {verdict && (
          <span className="small muted">
            {ok} de {verdict.faults.length} com nota de resiliente
          </span>
        )}
      </div>
      <table>
        <thead>
          <tr>
            <th>Falha</th>
            <th>Aplicada → revertida</th>
            <th>Estado</th>
            {verdict && (
              <>
                <th title="Segundos dentro dos SLOs enquanto a falha estava ativa">Dentro dos SLOs</th>
                <th title="Do fim da falha até 3 s seguidos dentro dos SLOs">Recuperação</th>
                <th>Nota</th>
              </>
            )}
          </tr>
        </thead>
        <tbody>
          {bands.map(({ fault: f, ev }) => (
            <tr key={f.id}>
              <td>
                <span className="fault-name">
                  <span className="swatch" style={{ ['--c' as string]: faultMeta[f.type].color }} /> {faultSummary(f)}
                </span>
                <div className="small muted">
                  planejada {f.startSeconds} s → {f.startSeconds + f.durationSeconds} s
                </div>
              </td>
              <td className="mono small">
                {secs(ev?.appliedAt)} → {secs(ev?.revertedAt)}
              </td>
              <td>
                <span className={`state ${ev?.state ?? ''}`}>{ev ? faultStateLabel[ev.state] : 'aguardando'}</span>
                {ev?.error && <div className="small error">{ev.error}</div>}
              </td>
              {verdict && <FaultVerdictCells r={result(f.id)} />}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

function FaultVerdictCells({ r }: { r?: FaultResult }) {
  if (!r) return <td colSpan={3} className="muted">—</td>;
  const worst = [
    r.worstP95Ms !== undefined ? `pior latência ${r.worstP95Ms.toLocaleString('pt-BR')} ms` : '',
    r.worstErrorRatePct ? `erro até ${r.worstErrorRatePct.toLocaleString('pt-BR')}%` : '',
    r.stalledSeconds ? `${r.stalledSeconds} s sem resposta` : '',
    r.probeFailures ? `${r.probeFailures} sondas falharam` : '',
  ]
    .filter(Boolean)
    .join(' · ');
  const pct = r.during.compliancePct;
  return (
    <>
      <td>
        {r.during.seconds ? (
          <div className="conf">
            <div className="conf-bar">
              <div className={gradeOf(pct)} style={{ width: `${pct}%` }} />
            </div>
            <span>{n(pct)}%</span>
          </div>
        ) : (
          '—'
        )}
        {worst && <div className="small muted">{worst}</div>}
      </td>
      <td>{r.recovered ? `${r.recoverySeconds} s` : <span className="error">não recuperou</span>}</td>
      <td>
        <span className={`score-pill ${gradeOf(r.score)}`}>{r.score}</span>
      </td>
    </>
  );
}

const gradeLabel: Record<Grade, string> = {
  resiliente: 'Resiliente',
  atencao: 'Atenção',
  fragil: 'Frágil',
  sem_dados: 'Sem dados',
};

const gradeOf = (score: number): Grade => (score >= 90 ? 'resiliente' : score >= 70 ? 'atencao' : 'fragil');

function VerdictPanel({ run, onReference, onAI }: { run: RunDetail; onReference: (id: string) => void; onAI: () => void }) {
  const v = run.verdict!;
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const isRef = run.referenceRunId === run.id;
  const toggle = async () => {
    setBusy(true);
    setError(null);
    try {
      const r = await api.setReference(run.scenarioId, isRef ? '' : run.id);
      onReference(r.referenceRunId);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };
  const isChaos = (run.scenario.mode || 'load') !== 'load';
  return (
    <div className="card">
      <div className="card-head">
        <h2>Veredito</h2>
        <button className={isRef ? 'ref-on' : ''} disabled={busy} onClick={toggle} title="A IA compara as próximas análises deste cenário com a execução de referência">
          <Icon name="star" size={16} />
          {isRef ? 'Referência do cenário' : 'Marcar como referência'}
        </button>
      </div>
      {run.referenceRunId && !isRef && (
        <p className="small muted">
          Referência deste cenário: <a href={`#/execucoes/${run.referenceRunId}`}>outra execução</a>.
        </p>
      )}
      {error && <p className="error">{error}</p>}
      <ul className="checks">
        {v.checks.map((c) => (
          <li key={c.name} className={c.pass ? 'pass' : 'fail'}>
            <Icon name={c.pass ? 'check' : 'x'} />
            <span>
              <b>{c.name}</b> {c.target}: {c.actual}
            </span>
          </li>
        ))}
      </ul>
      <div className="verdict-notes small muted">
        {isChaos && v.baseline.seconds > 0 && <p>Fora das falhas: {v.baseline.compliancePct.toLocaleString('pt-BR')}% dos segundos dentro dos SLOs.</p>}
        {v.notes?.map((note) => <p key={note}>{note}</p>)}
        <p>
          {isChaos
            ? 'Nota = 20% linha de base + 80% falhas; cada falha vale 70% pelo tempo dentro dos SLOs e 30% pela recuperação (cheia até 5 s, zero a partir de 60 s).'
            : 'Nota = porcentagem dos segundos dentro dos SLOs.'}{' '}
          {run.windows.length > 0 && (
            <button className="link no-print" onClick={onAI}>
              Ver análise da IA
            </button>
          )}
        </p>
      </div>
    </div>
  );
}

function ProbeSummary({ windows }: { windows: Window[] }) {
  const s = useMemo(() => {
    const probes = windows.flatMap((w) => (w.probe ? [w.probe] : []));
    const ok = probes.filter((p) => p.ok);
    const ms = ok.map((p) => p.ms).sort((a, b) => a - b);
    return {
      total: probes.length,
      avail: probes.length ? ok.length / probes.length : 0,
      failed: probes.length - ok.length,
      med: ms.length ? ms[Math.floor(ms.length / 2)] : 0,
      worst: ms.length ? ms[ms.length - 1] : 0,
      cpuMax: Math.max(0, ...windows.map((w) => w.cpu)),
      memMax: Math.max(0, ...windows.map((w) => w.memMb)),
    };
  }, [windows]);
  const series = (f: (w: Window) => number | null) => windows.map((w) => f(w));
  return (
    <div className="tiles">
      <Tile label="Disponibilidade" value={`${n(s.avail * 100, 1)}%`} hint={`${s.total} sondas`} bad={s.total > 0 && s.avail < 0.99} spark={series((w) => (w.probe ? (w.probe.ok ? 1 : 0) : null))} />
      <Tile label="Sondas com falha" value={n(s.failed)} hint="sem resposta ou status ≥ 400" bad={s.failed > 0} />
      <Tile label="Latência típica" value={`${n(s.med, 1)} ms`} hint={`pior resposta: ${n(s.worst, 1)} ms`} spark={series((w) => (w.probe?.ok ? w.probe.ms : null))} />
      <Tile label="CPU do alvo (pico)" value={`${n(s.cpuMax, 1)}%`} hint="100% = 1 núcleo" spark={series((w) => w.cpu)} />
      <Tile label="Memória do alvo (pico)" value={`${n(s.memMax, 1)} MB`} spark={series((w) => w.memMb)} />
    </div>
  );
}

// Mini gráfico de tendência ao pé do indicador (sem eixos; o detalhe fica na aba Gráficos).
function Spark({ values }: { values: (number | null)[] }) {
  const pts = values.map((v, i) => [i, v] as const).filter((p): p is readonly [number, number] => p[1] !== null && Number.isFinite(p[1]));
  if (pts.length < 2) return null;
  const max = Math.max(...pts.map((p) => p[1]));
  const min = Math.min(0, ...pts.map((p) => p[1]));
  const span = max - min || 1;
  const last = values.length - 1 || 1;
  const d = pts.map(([i, v], k) => `${k ? 'L' : 'M'}${((i / last) * 120).toFixed(1)} ${(25 - ((v - min) / span) * 23).toFixed(1)}`).join(' ');
  return (
    <svg className="spark" viewBox="0 0 120 26" preserveAspectRatio="none" aria-hidden="true">
      <path d={d} fill="none" stroke="var(--series-1)" strokeWidth={1.6} vectorEffect="non-scaling-stroke" strokeLinejoin="round" />
    </svg>
  );
}

function Tile({ label, value, hint, bad, spark }: { label: string; value: string; hint?: string; bad?: boolean; spark?: (number | null)[] }) {
  return (
    <div className={`tile ${bad ? 'bad' : ''}`}>
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      {hint && <div className="tile-hint">{hint}</div>}
      {spark && <Spark values={spark} />}
    </div>
  );
}

const axis = { stroke: 'var(--axis)', tick: { fill: 'var(--text-secondary)', fontSize: 12 } };
const tooltipStyle = {
  contentStyle: { background: 'var(--surface-1)', border: '1px solid var(--border)', borderRadius: 8, boxShadow: '0 4px 12px rgba(0,0,0,.12)', fontSize: 13 },
  cursor: { stroke: 'var(--text-secondary)', strokeDasharray: '3 3' },
  labelStyle: { color: 'var(--text-primary)' },
  itemStyle: { color: 'var(--text-primary)' },
  labelFormatter: (t: number) => `${t} s`,
};

function ChartCard({ title, children, wide, height = 220, top }: { title: string; children: ReactNode; wide?: boolean; height?: number; top?: ReactNode }) {
  return (
    <div className={`chart ${wide ? 'wide' : ''}`}>
      <div className="chart-title">{title}</div>
      {top}
      <ResponsiveContainer width="100%" height={height}>
        {children as React.ReactElement}
      </ResponsiveContainer>
    </div>
  );
}

// Trechos contíguos de segundos presos, como [início, fim).
function stalls(windows: Window[]): [number, number][] {
  const out: [number, number][] = [];
  for (const w of windows) {
    if (!w.stalled) continue;
    const last = out[out.length - 1];
    if (last && last[1] === w.t) last[1] = w.t + 1;
    else out.push([w.t, w.t + 1]);
  }
  return out;
}

function Charts({ windows, bands, chaosOnly, duration, k8s }: { windows: Window[]; bands: Band[]; chaosOnly: boolean; duration: number; k8s: boolean }) {
  if (windows.length === 0) return <p className="muted">Aguardando as primeiras métricas…</p>;
  const data = windows.map((w) => ({
    ...w,
    errPct: w.errorRate === null ? null : Math.round(w.errorRate * 10000) / 100,
    probeMs: w.probe?.ok ? w.probe.ms : null,
    probeOk: w.probe?.ok ? 1 : 0,
    probeFail: w.probe && !w.probe.ok ? 1 : 0,
    probeInfo: w.probe ? (w.probe.ok ? `${w.probe.status}` : w.probe.error || `${w.probe.status}`) : '',
    podsReady: w.pods?.ready ?? null,
    podsTotal: w.pods?.total ?? null,
  }));
  const target = k8s ? 'dos pods do Deployment' : 'do container alvo';
  const last = windows[windows.length - 1].t;
  const common = { data, syncId: 'run', margin: { top: 8, right: 16, bottom: 0, left: 0 } };
  const x = <XAxis dataKey="t" type="number" domain={[0, Math.max(duration, last)]} unit=" s" {...axis} />;
  const grid = <CartesianGrid stroke="var(--grid)" vertical={false} />;
  // Faixas das falhas atrás das séries; o rótulo só no primeiro gráfico para não poluir.
  const areas = (labeled = false) =>
    bands.map((b) => (
      <ReferenceArea
        key={b.fault.id}
        x1={b.x1}
        x2={b.x2}
        ifOverflow="extendDomain"
        fill={faultMeta[b.fault.type].color}
        // Ainda não aplicada: faixa tracejada e mais clara (é só o planejado).
        fillOpacity={b.ev ? 0.14 : 0.05}
        stroke={faultMeta[b.fault.type].color}
        strokeOpacity={0.5}
        strokeDasharray={b.ev ? undefined : '4 3'}
        label={labeled ? { value: faultMeta[b.fault.type].short, position: 'insideTopLeft', fill: 'var(--text-secondary)', fontSize: 12 } : undefined}
      />
    ));
  // Trechos sem nenhuma resposta (alvo fora do ar): faixa vermelha, já que ali não há latência para desenhar.
  const stalledAreas = (labeled = false) =>
    stalls(windows).map(([a, b]) => (
      <ReferenceArea
        key={`stall-${a}`}
        x1={a}
        x2={b}
        ifOverflow="extendDomain"
        fill="var(--status-critical)"
        fillOpacity={0.12}
        label={labeled ? { value: 'sem resposta', position: 'insideBottom', fill: 'var(--status-critical)', fontSize: 12 } : undefined}
      />
    ));
  const line = (key: string, name: string, color: string, unit = '') => (
    <Line
      type="monotone"
      dataKey={key}
      name={name}
      unit={unit}
      stroke={color}
      strokeWidth={2}
      dot={false}
      isAnimationActive={false}
    />
  );
  const domainMax = Math.max(duration, last) || 1;
  // Faixa com os blocos das falhas, alinhada à área do gráfico de cima (eixo Y de 48 px à esquerda, margem de 16 à direita).
  const strip =
    bands.length > 0 ? (
      <>
        <div className="legend" style={{ margin: '0 10px 8px' }}>
          {[...new Set(bands.map((b) => b.fault.type))].map((t) => (
            <span key={t}>
              <span className="swatch" style={{ ['--c' as string]: faultMeta[t].color }} />
              {faultMeta[t].short}
            </span>
          ))}
          <span>Passe o mouse num gráfico: o mesmo instante aparece em todos.</span>
        </div>
        <div className="fault-strip">
          {bands.map((b) => (
            <div
              key={b.fault.id}
              className={b.ev ? '' : 'planned'}
              title={faultSummary(b.fault)}
              style={{ ['--c' as string]: faultMeta[b.fault.type].color, left: `${(b.x1 / domainMax) * 100}%`, width: `${((Math.min(b.x2, domainMax) - b.x1) / domainMax) * 100}%` }}
            >
              {faultMeta[b.fault.type].short}
            </div>
          ))}
        </div>
      </>
    ) : undefined;
  const resources = (
    <>
      <ChartCard title={`CPU ${target} (%, 100 = 1 núcleo)`}>
        <LineChart {...common}>
          {grid}
          {x}
          {areas()}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('cpu', 'CPU', 'var(--series-1)', '%')}
        </LineChart>
      </ChartCard>
      <ChartCard title={`Memória ${target} (MB)`}>
        <LineChart {...common}>
          {grid}
          {x}
          {areas()}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('memMb', 'Memória', 'var(--series-1)', ' MB')}
        </LineChart>
      </ChartCard>
      {k8s && (
        <ChartCard title="Pods do Deployment" wide height={160}>
          <LineChart {...common}>
            {grid}
            {x}
            {areas()}
            <YAxis width={48} allowDecimals={false} domain={[0, 'dataMax + 1']} {...axis} />
            <Tooltip {...tooltipStyle} />
            <Legend verticalAlign="top" align="right" height={24} />
            <Line type="stepAfter" dataKey="podsTotal" name="Existentes" stroke="var(--status-neutral)" strokeWidth={2} strokeDasharray="4 3" dot={false} isAnimationActive={false} />
            <Line type="stepAfter" dataKey="podsReady" name="Prontos" stroke="var(--series-1)" strokeWidth={2} dot={false} isAnimationActive={false} />
          </LineChart>
        </ChartCard>
      )}
    </>
  );

  if (chaosOnly) {
    return (
      <div className="charts">
        <ChartCard title="Latência da sonda (ms, só respostas com sucesso)" wide top={strip}>
          <LineChart {...common}>
            {grid}
            {x}
            {areas()}
            <YAxis width={48} {...axis} />
            <Tooltip {...tooltipStyle} />
            <Line type="monotone" dataKey="probeMs" name="Latência" unit=" ms" stroke="var(--series-1)" strokeWidth={2} dot={false} connectNulls={false} isAnimationActive={false} />
          </LineChart>
        </ChartCard>
        <ChartCard title="Resultado da sonda (1 por segundo)" wide height={110}>
          <BarChart {...common} barCategoryGap={1}>
            {grid}
            {x}
            {areas()}
            <YAxis width={48} domain={[0, 1]} tick={false} {...{ stroke: axis.stroke }} />
            <Tooltip
              {...tooltipStyle}
              cursor={{ fill: 'var(--grid)' }}
              formatter={(v: number, name: string, item: { payload?: { probeInfo?: string } }) => (v ? [item.payload?.probeInfo ?? '', name] : [null, null])}
            />
            <Legend verticalAlign="top" align="right" height={24} />
            <Bar dataKey="probeOk" name="Respondeu" stackId="p" fill="var(--status-good)" isAnimationActive={false} />
            <Bar dataKey="probeFail" name="Falhou" stackId="p" fill="var(--status-critical)" isAnimationActive={false} />
          </BarChart>
        </ChartCard>
        {resources}
      </div>
    );
  }

  return (
    <div className="charts">
      <ChartCard title="Latência (ms)" wide top={strip}>
        <LineChart {...common}>
          {grid}
          {x}
          {areas()}
          {stalledAreas(true)}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          <Legend />
          {line('p50', 'p50', 'var(--series-1)', ' ms')}
          {line('p95', 'p95', 'var(--series-2)', ' ms')}
          {line('p99', 'p99', 'var(--series-3)', ' ms')}
        </LineChart>
      </ChartCard>
      <ChartCard title="Requisições por segundo">
        <LineChart {...common}>
          {grid}
          {x}
          {areas()}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('rps', 'RPS', 'var(--series-1)')}
        </LineChart>
      </ChartCard>
      <ChartCard title="Usuários virtuais ativos">
        <LineChart {...common}>
          {grid}
          {x}
          {areas()}
          <YAxis width={48} allowDecimals={false} {...axis} />
          <Tooltip {...tooltipStyle} />
          <Line type="stepAfter" dataKey="vus" name="VUs" stroke="var(--series-1)" strokeWidth={2} dot={false} isAnimationActive={false} />
        </LineChart>
      </ChartCard>
      <ChartCard title="Respostas por código HTTP">
        <BarChart {...common} barCategoryGap={1}>
          {grid}
          {x}
          {areas()}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} cursor={{ fill: 'var(--grid)' }} />
          <Legend />
          <Bar dataKey="s2xx" name="2xx" stackId="s" fill="var(--status-good)" isAnimationActive={false} />
          <Bar dataKey="s3xx" name="3xx" stackId="s" fill="var(--status-neutral)" isAnimationActive={false} />
          <Bar dataKey="s4xx" name="4xx" stackId="s" fill="var(--status-warning)" isAnimationActive={false} />
          <Bar dataKey="s5xx" name="5xx" stackId="s" fill="var(--status-critical)" isAnimationActive={false} />
          <Bar dataKey="sErr" name="Sem resposta" stackId="s" fill="var(--status-serious)" isAnimationActive={false} />
        </BarChart>
      </ChartCard>
      <ChartCard title="Taxa de erro (%)">
        <LineChart {...common}>
          {grid}
          {x}
          {areas()}
          {stalledAreas()}
          <YAxis width={48} domain={[0, 'auto']} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('errPct', 'Erro', 'var(--status-critical)', '%')}
        </LineChart>
      </ChartCard>
      {resources}
    </div>
  );
}
