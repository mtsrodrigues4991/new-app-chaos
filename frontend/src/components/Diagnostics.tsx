import { useEffect, useMemo, useState } from 'react';
import { api, Diagnostics as Diag, PodLog } from '../api';

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
  if (!diag || (diag.logs.length === 0 && diag.events.length === 0 && !diag.notes?.length)) return null;
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
