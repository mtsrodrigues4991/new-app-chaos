import { useEffect, useMemo, useState } from 'react';
import { CartesianGrid, Legend, Line, LineChart, ReferenceArea, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import { api, AppTelemetry, Diagnostics as Diag, PodLog } from '../api';

// Linhas que costumam explicar uma falha (o mesmo critério que o backend usa para a IA).
const important = /error|erro|exception|fatal|panic|warn|fail|refused|reset|timeout|timed out|denied|killed|oom|caused by|sqlstate|unavailable|terminat|shutdown/i;
const MAX_LINES = 2000;

export type FaultSpan = { label: string; from: number; to: number };

const fmtT = (t: number) => (t < 0 ? '?' : t.toFixed(1));

function faultAt(spans: FaultSpan[], t: number): string {
  return spans
    .filter((s) => t >= s.from && t < s.to)
    .map((s) => s.label)
    .join(' + ');
}

// Logs dos containers e eventos do Kubernetes coletados durante a execução.
export default function Diagnostics({ runId, spans }: { runId: string; spans: FaultSpan[] }) {
  const [diag, setDiag] = useState<Diag | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .diagnostics(runId)
      .then(setDiag)
      .catch((e: Error) => setError(e.message));
  }, [runId]);

  if (error) return <p className="error no-print">Logs e eventos: {error}</p>;
  if (!diag) return null;
  return (
    <>
      {diag.app && <AppView app={diag.app} spans={spans} />}
      {(diag.logs.length > 0 || diag.events.length > 0 || !!diag.notes?.length) && <LogsAndEvents diag={diag} spans={spans} />}
    </>
  );
}

function LogsAndEvents({ diag, spans }: { diag: Diag; spans: FaultSpan[] }) {
  return (
    <section className="diagnostics no-print">
      <h2>Logs e eventos</h2>
      <p className="muted small">
        Coletados durante a execução, do alvo e das dependências usadas nas falhas. A IA recebe um resumo deles: linhas repetidas juntadas, com
        prioridade para erros, conexões, partidas e paradas. O t é em segundos desde o início, como nos gráficos.
      </p>
      {diag.notes?.map((n) => (
        <p key={n} className="error small">
          {n}
        </p>
      ))}
      {diag.events.length > 0 && (
        <details open={diag.events.length <= 15}>
          <summary>
            Eventos do Kubernetes ({diag.events.length}
            {diag.events.some((e) => e.type === 'Warning') && `, ${diag.events.filter((e) => e.type === 'Warning').length} Warning`})
          </summary>
          <table className="small">
            <thead>
              <tr>
                <th>t</th>
                <th>Falha ativa</th>
                <th>Tipo</th>
                <th>Motivo</th>
                <th>Objeto</th>
                <th>Mensagem</th>
              </tr>
            </thead>
            <tbody>
              {diag.events.map((e, i) => (
                <tr key={i}>
                  <td>{fmtT(e.t)}</td>
                  <td className="muted">{faultAt(spans, e.t)}</td>
                  <td className={e.type === 'Warning' ? 'error' : undefined}>{e.type}</td>
                  <td>{e.reason}</td>
                  <td>
                    {e.namespace}/{e.object}
                  </td>
                  <td>
                    {e.message}
                    {e.count && e.count > 1 ? <span className="muted"> ×{e.count}</span> : null}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
      {diag.logs.map((l) => (
        <LogView key={`${l.namespace}/${l.pod}/${l.container}/${l.previous}`} log={l} spans={spans} />
      ))}
    </section>
  );
}

function LogView({ log, spans }: { log: PodLog; spans: FaultSpan[] }) {
  const [onlyImportant, setOnlyImportant] = useState(false);
  const [filter, setFilter] = useState('');
  const errors = useMemo(() => log.lines.filter((l) => important.test(l.text)).length, [log]);
  const shown = useMemo(() => {
    const f = filter.toLowerCase();
    return log.lines.filter((l) => (!onlyImportant || important.test(l.text)) && (!f || l.text.toLowerCase().includes(f)));
  }, [log, onlyImportant, filter]);
  const tail = shown.slice(-MAX_LINES);

  const name = `${log.namespace ? log.namespace + '/' : ''}${log.pod}${log.container ? ` (${log.container})` : ''}`;
  const flags = [log.previous && 'instância anterior, antes de reiniciar', log.gone && 'pod apagado durante a execução', log.truncated && 'só as últimas linhas']
    .filter(Boolean)
    .join(' · ');
  return (
    <details className="log">
      <summary>
        <span className="muted">{log.role}</span> {name} · {log.lines.length} linhas
        {errors > 0 && <span className="error"> · {errors} com erro ou aviso</span>}
        {flags && <span className="muted"> · {flags}</span>}
      </summary>
      <div className="log-tools">
        <label className="check">
          <input type="checkbox" checked={onlyImportant} onChange={(e) => setOnlyImportant(e.target.checked)} /> Só erros e avisos
        </label>
        <input placeholder="Filtrar" value={filter} onChange={(e) => setFilter(e.target.value)} />
        {shown.length > MAX_LINES && <span className="muted small">mostrando as últimas {MAX_LINES} de {shown.length}</span>}
      </div>
      <pre className="code log-lines">
        {tail.length === 0 && <span className="muted">(nenhuma linha)</span>}
        {tail.map((l, i) => {
          const f = faultAt(spans, l.t);
          return (
            <div key={i} className={important.test(l.text) ? 'error' : undefined}>
              <span className="muted">{fmtT(l.t).padStart(6)} </span>
              {f && <span className="log-fault">[{f}] </span>}
              {l.text}
            </div>
          );
        })}
      </pre>
    </details>
  );
}

const axis = { stroke: 'var(--axis)', tick: { fill: 'var(--text-secondary)', fontSize: 12 } };
const tooltipStyle = {
  contentStyle: { background: 'var(--surface-2)', border: '1px solid var(--grid)', borderRadius: 6 },
  labelStyle: { color: 'var(--text-primary)' },
  itemStyle: { color: 'var(--text-primary)' },
  labelFormatter: (t: number) => `${t} s`,
};
const serviceColors = ['var(--series-1)', 'var(--series-2)', 'var(--series-3)'];

// Telemetria da aplicação vinda da stack de observabilidade (OBS=1): métricas, traces e profiling.
function AppView({ app: raw, spans }: { app: AppTelemetry; spans: FaultSpan[] }) {
  // Listas vazias podem vir como null do backend.
  const app = { ...raw, metrics: raw.metrics ?? [], traces: raw.traces ?? [], profiles: raw.profiles ?? [] };
  const charts = useMemo(() => {
    const byKey = new Map<string, { label: string; unit: string; services: string[]; rows: Map<number, Record<string, number>> }>();
    for (const m of app.metrics) {
      let c = byKey.get(m.key);
      if (!c) byKey.set(m.key, (c = { label: m.label, unit: m.unit, services: [], rows: new Map() }));
      if (!c.services.includes(m.service)) c.services.push(m.service);
      for (const p of m.points) {
        const row = c.rows.get(p.t) ?? { t: p.t };
        row[m.service] = p.v;
        c.rows.set(p.t, row);
      }
    }
    return [...byKey.entries()].map(([key, c]) => ({ key, ...c, data: [...c.rows.values()].sort((a, b) => a.t - b.t) }));
  }, [raw]);
  const empty = charts.length === 0 && app.traces.length === 0 && app.profiles.length === 0;
  return (
    <section className="diagnostics no-print">
      <h2>Telemetria da aplicação</h2>
      <p className="muted small">
        Da stack de observabilidade (Prometheus, Tempo e Pyroscope), do alvo e das dependências usadas nas falhas. A IA recebe tudo isso.
        {app.grafanaUrl && (
          <>
            {' '}
            Para explorar mais, abra o <a href={app.grafanaUrl} target="_blank" rel="noreferrer">Grafana</a> (com{' '}
            <code>kubectl -n observability port-forward svc/lgtm 3001:3000</code> rodando).
          </>
        )}
      </p>
      {app.notes?.map((n) => (
        <p key={n} className="muted small">
          {n}
        </p>
      ))}
      {empty && <p className="muted">Nada chegou da aplicação nesta execução.</p>}
      {charts.length > 0 && (
        <p className="muted small">
          As taxas e o p95 aparecem uns 5 a 15 s depois do que a sonda mostra: o tempo de uma requisição só é registrado quando ela termina, e a taxa
          é calculada numa janela de 10 s. A faixa cinza é a falha.
        </p>
      )}
      {charts.length > 0 && (
        <div className="charts">
          {charts.map((c) => (
            <div key={c.key} className="chart">
              <div className="chart-title">
                {c.label}
                {c.unit && ` (${c.unit})`}
              </div>
              <ResponsiveContainer width="100%" height={180}>
                <LineChart data={c.data} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
                  <CartesianGrid stroke="var(--grid)" vertical={false} />
                  <XAxis dataKey="t" type="number" domain={['dataMin', 'dataMax']} unit=" s" {...axis} />
                  <YAxis width={48} {...axis} />
                  {spans.map((s) => (
                    <ReferenceArea key={`${s.label}-${s.from}`} x1={s.from} x2={s.to} ifOverflow="extendDomain" fill="var(--text-secondary)" fillOpacity={0.08} />
                  ))}
                  <Tooltip {...tooltipStyle} />
                  {c.services.length > 1 && <Legend />}
                  {c.services.map((svc, i) => (
                    <Line key={svc} type="linear" dataKey={svc} name={svc} stroke={serviceColors[i % serviceColors.length]} strokeWidth={2} dot={false} isAnimationActive={false} connectNulls />
                  ))}
                </LineChart>
              </ResponsiveContainer>
            </div>
          ))}
        </div>
      )}
      {app.traces.length > 0 && (
        <details open>
          <summary>Traces com erro e lentos ({app.traces.length})</summary>
          <table className="small traces">
            <thead>
              <tr>
                <th>t</th>
                <th>Falha ativa</th>
                <th>Tipo</th>
                <th>Serviço</th>
                <th>Requisição</th>
                <th>Duração</th>
                <th>Onde o tempo foi gasto</th>
              </tr>
            </thead>
            <tbody>
              {app.traces.map((t) => (
                <tr key={t.traceId}>
                  <td>{fmtT(t.t)}</td>
                  <td className="muted">{faultAt(spans, t.t)}</td>
                  <td className={t.kind === 'erro' ? 'error' : undefined}>{t.kind}</td>
                  <td>{t.service}</td>
                  <td>
                    {t.root}
                    <div className="muted small">trace {t.traceId}</div>
                  </td>
                  <td>{Math.round(t.durationMs)} ms</td>
                  <td>
                    {(t.spans ?? []).slice(0, 4).map((sp, i) => (
                      <div key={i} className={sp.error ? 'error' : undefined}>
                        {sp.name}: {Math.round(sp.durationMs)} ms{sp.detail && <span className="muted"> · {sp.detail}</span>}
                      </div>
                    ))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </details>
      )}
      {app.profiles.length > 0 && (
        <details>
          <summary>Profiling: funções que mais consumiram na execução (sem o próprio agente)</summary>
          {app.profiles.map((p) => (
            <div key={`${p.service}-${p.type}`}>
              <h3 className="small">
                {p.service} · {p.type}
              </h3>
              <table className="small">
                <tbody>
                  {p.functions.map((f) => (
                    <tr key={f.name}>
                      <td style={{ width: 80 }}>{f.selfPct}%</td>
                      <td>
                        <code>{f.name}</code>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          ))}
        </details>
      )}
    </section>
  );
}
