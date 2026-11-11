import { ReactNode, useEffect, useMemo, useState } from 'react';
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from 'recharts';
import { api, LiveEvent, Run, RunDetail, RunStatus, Window } from '../api';
import { profileLabel } from './ScenarioEditor';

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

const live = (s: RunStatus) => s === 'pending' || s === 'running';

function mergeWindows(prev: Window[], next: Window[]): Window[] {
  const byT = new Map(prev.map((w) => [w.t, w]));
  for (const w of next) byT.set(w.t, w);
  return [...byT.values()].sort((a, b) => a.t - b.t);
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
  return (
    <section>
      <div className="title-row">
        <h1>{run.scenarioName}</h1>
        <StatusBadge status={run.status} />
        {live(run.status) && (
          <button className="danger" disabled={aborting} onClick={abort}>
            {aborting ? 'Abortando…' : 'Abortar'}
          </button>
        )}
      </div>
      <p className="muted">
        {run.scenario.target.baseUrl} · {profileLabel[p.type]}, {p.vus} VUs, {p.durationSeconds} s · iniciada em{' '}
        {new Date(run.createdAt).toLocaleString('pt-BR')} · duração {duration(run)} ·{' '}
        <a href={`#/cenarios/${run.scenarioId}`}>editar cenário</a>
      </p>
      {run.status === 'pending' && <p className="muted">Preparando o k6 (na primeira vez ele baixa a imagem, pode levar um minuto)…</p>}
      {run.error && <p className="error">{run.error}</p>}
      {actionError && <p className="error">Não foi possível abortar: {actionError}</p>}
      <Summary windows={run.windows} />
      <Charts windows={run.windows} />
    </section>
  );
}

function Summary({ windows }: { windows: Window[] }) {
  const s = useMemo(() => {
    const active = windows.filter((w) => w.rps > 0);
    const reqs = windows.reduce((a, w) => a + w.rps, 0);
    const failed = windows.reduce((a, w) => a + w.errorRate * w.rps, 0);
    const p95s = active.map((w) => w.p95).sort((a, b) => a - b);
    return {
      reqs,
      rps: active.length ? reqs / active.length : 0,
      p95: p95s.length ? p95s[Math.floor(p95s.length / 2)] : 0,
      p95max: p95s.length ? p95s[p95s.length - 1] : 0,
      errRate: reqs ? failed / reqs : 0,
      cpuMax: Math.max(0, ...windows.map((w) => w.cpu)),
      memMax: Math.max(0, ...windows.map((w) => w.memMb)),
    };
  }, [windows]);
  const n = (v: number, d = 0) => v.toLocaleString('pt-BR', { maximumFractionDigits: d, minimumFractionDigits: d });
  return (
    <div className="tiles">
      <Tile label="Requisições" value={n(s.reqs)} />
      <Tile label="RPS médio" value={n(s.rps, 1)} />
      <Tile label="p95 típico" value={`${n(s.p95, 1)} ms`} hint={`pior janela: ${n(s.p95max, 1)} ms`} />
      <Tile label="Taxa de erro" value={`${n(s.errRate * 100, 2)}%`} />
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

function ChartCard({ title, children, wide }: { title: string; children: ReactNode; wide?: boolean }) {
  return (
    <div className={`chart ${wide ? 'wide' : ''}`}>
      <div className="chart-title">{title}</div>
      <ResponsiveContainer width="100%" height={220}>
        {children as React.ReactElement}
      </ResponsiveContainer>
    </div>
  );
}

function Charts({ windows }: { windows: Window[] }) {
  if (windows.length === 0) return <p className="muted">Aguardando as primeiras métricas…</p>;
  const data = windows.map((w) => ({ ...w, errPct: Math.round(w.errorRate * 10000) / 100 }));
  const common = { data, syncId: 'run', margin: { top: 8, right: 16, bottom: 0, left: 0 } };
  const x = <XAxis dataKey="t" type="number" domain={['dataMin', 'dataMax']} unit=" s" {...axis} />;
  const grid = <CartesianGrid stroke="var(--grid)" vertical={false} />;
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
  return (
    <div className="charts">
      <ChartCard title="Latência (ms)" wide>
        <LineChart {...common}>
          {grid}
          {x}
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
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('rps', 'RPS', 'var(--series-1)')}
        </LineChart>
      </ChartCard>
      <ChartCard title="Usuários virtuais ativos">
        <LineChart {...common}>
          {grid}
          {x}
          <YAxis width={48} allowDecimals={false} {...axis} />
          <Tooltip {...tooltipStyle} />
          <Line type="stepAfter" dataKey="vus" name="VUs" stroke="var(--series-1)" strokeWidth={2} dot={false} isAnimationActive={false} />
        </LineChart>
      </ChartCard>
      <ChartCard title="Respostas por código HTTP">
        <BarChart {...common} barCategoryGap={1}>
          {grid}
          {x}
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
          <YAxis width={48} domain={[0, 'auto']} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('errPct', 'Erro', 'var(--status-critical)', '%')}
        </LineChart>
      </ChartCard>
      <ChartCard title="CPU do container alvo (%, 100 = 1 núcleo)">
        <LineChart {...common}>
          {grid}
          {x}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('cpu', 'CPU', 'var(--series-1)', '%')}
        </LineChart>
      </ChartCard>
      <ChartCard title="Memória do container alvo (MB)">
        <LineChart {...common}>
          {grid}
          {x}
          <YAxis width={48} {...axis} />
          <Tooltip {...tooltipStyle} />
          {line('memMb', 'Memória', 'var(--series-1)', ' MB')}
        </LineChart>
      </ChartCard>
    </div>
  );
}
