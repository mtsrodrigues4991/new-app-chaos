import { ReactNode, useEffect, useMemo, useState } from 'react';
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

const statusIcon: Record<RunStatus, string> = {
  pending: '…',
  running: '●',
  completed: '✓',
  aborted: '■',
  failed: '✕',
};

export function StatusBadge({ status }: { status: RunStatus }) {
  return (
    <span className={`badge ${status}`}>
      {statusIcon[status]} {statusLabel[status]}
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

export default function RunView({ id }: { id: string }) {
  const [run, setRun] = useState<RunDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [aborting, setAborting] = useState(false);
  const [actionError, setActionError] = useState<string | null>(null);

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

  if (error) return <p className="error">Erro: {error}</p>;
  if (!run) return <p>Carregando…</p>;

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
  return (
    <section className="run-view">
      <p className="print-only small muted">
        new-app-chaos · relatório da execução {run.id} · impresso em {new Date().toLocaleString('pt-BR')}
      </p>
      <div className="title-row">
        <h1>{run.scenarioName}</h1>
        <StatusBadge status={run.status} />
        {!live(run.status) && (
          <button className="no-print" onClick={printReport}>
            Imprimir / salvar PDF
          </button>
        )}
        {live(run.status) && (
          <button className="danger" disabled={aborting} onClick={abort}>
            {aborting ? 'Abortando…' : 'Abortar'}
          </button>
        )}
      </div>
      <p className="muted">
        {modeLabel[mode]} · {targetName(run.scenario.target)} · {run.scenario.target.baseUrl}
        {mode === 'chaos' ? ` · sonda ${run.scenario.probePath}` : ` · ${profileLabel[p.type]}, ${p.vus} VUs`} · {p.durationSeconds} s · iniciada em{' '}
        {new Date(run.createdAt).toLocaleString('pt-BR')} · duração {duration(run)} ·{' '}
        <a className="no-print" href={`#/cenarios/${run.scenarioId}`}>editar cenário</a>
      </p>
      {run.status === 'pending' && mode !== 'chaos' && <p className="muted">Preparando o k6 (na primeira vez ele baixa a imagem, pode levar um minuto)…</p>}
      {run.error && <p className="error">{run.error}</p>}
      {actionError && <p className="error">Não foi possível abortar: {actionError}</p>}
      {mode === 'chaos' ? <ProbeSummary windows={run.windows} /> : <Summary windows={run.windows} />}
      {run.verdict && <VerdictPanel run={run} onReference={(referenceRunId) => setRun({ ...run, referenceRunId })} />}
      {bs.length > 0 && <FaultList bands={bs} verdict={run.verdict} />}
      <Charts windows={run.windows} bands={bs} chaosOnly={mode === 'chaos'} duration={p.durationSeconds} k8s={targetPlatform(run.scenario.target) === 'kubernetes'} />
      {run.verdict && run.windows.length > 0 && <AIPanel run={run} onReport={(analysis) => setRun({ ...run, analysis })} />}
    </section>
  );
}

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
  const n = (v: number, d = 0) => v.toLocaleString('pt-BR', { maximumFractionDigits: d, minimumFractionDigits: d });
  return (
    <div className="tiles">
      <Tile label="Requisições" value={n(s.reqs)} />
      <Tile label="RPS médio" value={n(s.rps, 1)} />
      <Tile label="p95 típico" value={`${n(s.p95, 1)} ms`} hint={`pior janela: ${n(s.p95max, 1)} ms`} />
      <Tile label="Taxa de erro" value={`${n(s.errRate * 100, 2)}%`} />
      {s.stalled > 0 && <Tile label="Segundos sem resposta" value={`${s.stalled} s`} hint="usuários ativos e nada respondeu" />}
      <Tile label="CPU do alvo (pico)" value={`${n(s.cpuMax, 1)}%`} hint="100% = 1 núcleo" />
      <Tile label="Memória do alvo (pico)" value={`${n(s.memMax, 1)} MB`} />
    </div>
  );
}

const faultStateLabel: Record<FaultState, string> = {
  active: '● ativa agora',
  reverted: '✓ revertida',
  failed: '✕ falhou',
  skipped: '— não executada',
};

const secs = (v?: number) => (v === undefined ? '—' : `${v.toLocaleString('pt-BR', { maximumFractionDigits: 1 })} s`);

function FaultList({ bands, verdict }: { bands: Band[]; verdict?: Verdict }) {
  const result = (id: string) => verdict?.faults.find((r) => r.faultId === id);
  return (
    <table className="fault-list">
      <thead>
        <tr>
          <th>Falha</th>
          <th>Planejada</th>
          <th>Aplicada em</th>
          <th>Revertida em</th>
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
              <span className="swatch" style={{ ['--c' as string]: faultMeta[f.type].color }} /> {faultSummary(f)}
            </td>
            <td>
              {f.startSeconds} s → {f.startSeconds + f.durationSeconds} s
            </td>
            <td>{secs(ev?.appliedAt)}</td>
            <td>{secs(ev?.revertedAt)}</td>
            <td className={ev?.state === 'failed' ? 'error' : ''}>
              {ev ? faultStateLabel[ev.state] : 'aguardando'}
              {ev?.error && <div className="small">{ev.error}</div>}
            </td>
            {verdict && <FaultVerdictCells r={result(f.id)} />}
          </tr>
        ))}
      </tbody>
    </table>
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
  return (
    <>
      <td>
        {r.during.seconds ? `${r.during.compliancePct.toLocaleString('pt-BR')}%` : '—'}
        {worst && <div className="small muted">{worst}</div>}
      </td>
      <td>{r.recovered ? `${r.recoverySeconds} s` : <span className="error">não recuperou</span>}</td>
      <td>
        <strong className={`grade-text ${gradeOf(r.score)}`}>{r.score}</strong>
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

function VerdictPanel({ run, onReference }: { run: RunDetail; onReference: (id: string) => void }) {
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
    <div className="verdict">
      <div className={`score ${v.grade}`}>
        <div className="score-value">{v.grade === 'sem_dados' ? '—' : v.score}</div>
        <div className="score-label">{gradeLabel[v.grade]}</div>
      </div>
      <div className="verdict-body">
        <div className="title-row">
          <h2>Veredito</h2>
          <button className={isRef ? 'ref-on' : ''} disabled={busy} onClick={toggle} title="A IA compara as próximas análises deste cenário com a execução de referência">
            {isRef ? '★ Referência do cenário' : '☆ Marcar como referência'}
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
              <span aria-hidden>{c.pass ? '✓' : '✕'}</span> {c.name} {c.target}: {c.actual}
            </li>
          ))}
          {isChaos && v.baseline.seconds > 0 && (
            <li className="muted">
              Fora das falhas: {v.baseline.compliancePct.toLocaleString('pt-BR')}% dos segundos dentro dos SLOs
            </li>
          )}
        </ul>
        {v.notes?.map((n) => (
          <p key={n} className="small muted">
            {n}
          </p>
        ))}
        <p className="small muted">
          {isChaos
            ? 'Nota = 20% linha de base + 80% falhas; cada falha vale 70% pelo tempo dentro dos SLOs e 30% pela recuperação (cheia até 5 s, zero a partir de 60 s).'
            : 'Nota = porcentagem dos segundos dentro dos SLOs.'}{' '}
          <a href="#analise">Ver análise da IA ↓</a>
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
  const n = (v: number, d = 0) => v.toLocaleString('pt-BR', { maximumFractionDigits: d, minimumFractionDigits: d });
  return (
    <div className="tiles">
      <Tile label="Disponibilidade" value={`${n(s.avail * 100, 1)}%`} hint={`${s.total} sondas`} />
      <Tile label="Sondas com falha" value={n(s.failed)} hint="sem resposta ou status ≥ 400" />
      <Tile label="Latência típica" value={`${n(s.med, 1)} ms`} hint={`pior resposta: ${n(s.worst, 1)} ms`} />
      <Tile label="CPU do alvo (pico)" value={`${n(s.cpuMax, 1)}%`} hint="100% = 1 núcleo" />
      <Tile label="Memória do alvo (pico)" value={`${n(s.memMax, 1)} MB`} />
    </div>
  );
}

function Tile({ label, value, hint }: { label: string; value: string; hint?: string }) {
  return (
    <div className="tile">
      <div className="tile-label">{label}</div>
      <div className="tile-value">{value}</div>
      {hint && <div className="tile-hint">{hint}</div>}
    </div>
  );
}

const axis = { stroke: 'var(--axis)', tick: { fill: 'var(--text-secondary)', fontSize: 12 } };
const tooltipStyle = {
  contentStyle: { background: 'var(--surface-2)', border: '1px solid var(--grid)', borderRadius: 6 },
  labelStyle: { color: 'var(--text-primary)' },
  itemStyle: { color: 'var(--text-primary)' },
  labelFormatter: (t: number) => `${t} s`,
};

function ChartCard({ title, children, wide, height = 220 }: { title: string; children: ReactNode; wide?: boolean; height?: number }) {
  return (
    <div className={`chart ${wide ? 'wide' : ''}`}>
      <div className="chart-title">{title}</div>
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
        <ChartCard title="Latência da sonda (ms, só respostas com sucesso)" wide>
          <LineChart {...common}>
            {grid}
            {x}
            {areas(true)}
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
      <ChartCard title="Latência (ms)" wide>
        <LineChart {...common}>
          {grid}
          {x}
          {areas(true)}
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
