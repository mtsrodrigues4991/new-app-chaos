import { useEffect, useState } from 'react';
import { api, FixAttempt, FixCycle, FixStatus, Outcome } from '../api';

const statusLabel: Record<FixStatus, string> = {
  awaiting_approval: 'Esperando aprovação',
  queued: 'Na fila',
  running: 'Rodando no sandbox',
  pr_open: 'PR em rascunho aberto',
  rejected: 'Recusada',
  failed: 'Não virou PR',
};

// Classes de cor reaproveitadas do estado das execuções.
const statusClass: Record<FixStatus, string> = {
  awaiting_approval: 'aborted',
  queued: 'running',
  running: 'running',
  pr_open: 'completed',
  rejected: '',
  failed: 'failed',
};

const when = (s?: string) => (s ? new Date(s).toLocaleString('pt-BR') : '—');

export default function Fixes() {
  const [items, setItems] = useState<FixAttempt[]>([]);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const load = () =>
      api
        .fixes()
        .then(setItems)
        .catch((e: Error) => setError(e.message));
    load();
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, []);
  return (
    <section>
      <h1>Correções</h1>
      <p className="muted">
        Uma correção sugerida pela IA pode virar um pull request em rascunho no repositório do alvo. Uma pessoa aprova a tentativa, o patch é
        aplicado num pod isolado, o build e os testes do repositório rodam, e só então o PR é aberto. O merge é sempre seu.
      </p>
      {error && <p className="error">Erro: {error}</p>}
      {items.length === 0 && !error && (
        <p className="muted">Nenhuma tentativa ainda. Na análise da IA de uma execução, use "Tentar esta correção" numa sugestão com patch.</p>
      )}
      {items.length > 0 && (
        <div className="card table-card">
          <table>
            <thead>
              <tr>
                <th>Criada</th>
                <th>Correção</th>
                <th>Repositório</th>
                <th>Estado</th>
                <th>PR</th>
              </tr>
            </thead>
            <tbody>
              {items.map((a) => (
                <tr key={a.id}>
                  <td>
                    <a href={`#/correcoes/${a.id}`}>{when(a.createdAt)}</a>
                  </td>
                  <td>{a.title}</td>
                  <td className="mono">{a.repo}</td>
                  <td>
                    <span className={`badge ${statusClass[a.status]}`}>{statusLabel[a.status]}</span>
                  </td>
                  <td>
                    {a.pr?.url ? (
                      <a href={a.pr.url} target="_blank" rel="noreferrer">
                        #{a.pr.number}
                      </a>
                    ) : (
                      '—'
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

export function FixView({ id }: { id: string }) {
  const [a, setA] = useState<FixAttempt | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    let stop = false;
    const load = () =>
      api
        .fix(id)
        .then((x) => {
          if (!stop) setA(x);
        })
        .catch((e: Error) => setError(e.message));
    load();
    const t = setInterval(load, 3000);
    return () => {
      stop = true;
      clearInterval(t);
    };
  }, [id]);
  if (!a) return error ? <p className="error">Erro: {error}</p> : null;
  const decide = async (approve: boolean) => {
    if (approve && !confirm('Aprovar esta tentativa? O patch vai ser aplicado num pod isolado, o build e os testes vão rodar e, se passarem, um PR em rascunho será aberto em ' + a.repo + '.')) return;
    setBusy(true);
    setError(null);
    try {
      setA({ ...a, ...(approve ? await api.approveFix(a.id) : await api.rejectFix(a.id)) });
    } catch (e) {
      setError((e as Error).message);
    }
    setBusy(false);
  };
  return (
    <section>
      <div className="crumbs">
        <nav aria-label="Trilha">
          <a href="#/correcoes">Correções</a>
          <span aria-hidden="true">/</span>
          <span className="current">{a.title}</span>
          <span className="mono">#{a.id.slice(0, 8)}</span>
        </nav>
      </div>
      <div className="card">
        <div className="card-head">
          <h2>{a.title}</h2>
          <span className={`badge ${statusClass[a.status]}`}>{statusLabel[a.status]}</span>
        </div>
        <p>{a.detail}</p>
        <p className="muted small">
          Execução <a href={`#/execucoes/${a.runId}`}>#{a.runId.slice(0, 8)}</a> · repositório <span className="mono">{a.repo}</span>, commit{' '}
          <code>{a.commit.slice(0, 12)}</code>, PR para <span className="mono">{a.baseBranch}</span> · build:{' '}
          <code>{a.testCommand}</code> em <code>{a.testImage}</code>
        </p>
        {(a.metric || a.expected) && (
          <p className="muted small">
            Deve melhorar: {a.metric || 'não informado'}
            {a.expected ? ` (${a.expected})` : ''}. É uma hipótese: só um experimento A/B mede o ganho.
          </p>
        )}
        <p className="muted small">
          Pedida por {a.createdBy || 'usuário local'} em {when(a.createdAt)}
          {a.decidedAt ? ` · ${a.status === 'rejected' ? 'recusada' : 'aprovada'} por ${a.decidedBy || 'usuário local'} em ${when(a.decidedAt)}` : ''}
          {a.costUsd > 0 ? ` · custo do agente: US$ ${a.costUsd.toFixed(2)}` : ''}
        </p>
        {a.error && <p className="error">{a.error}</p>}
        {error && <p className="error">Erro: {error}</p>}
        {a.pr?.url && (
          <p>
            <a className="button primary" href={a.pr.url} target="_blank" rel="noreferrer">
              Abrir o PR #{a.pr.number} (rascunho)
            </a>
          </p>
        )}
        {a.status === 'awaiting_approval' && (
          <>
            {a.blocked && <p className="notice">Ainda não dá para aprovar: {a.blocked}</p>}
            {a.canDecide ? (
              <div className="row" style={{ display: 'flex', gap: 12 }}>
                <button className="primary" disabled={busy || !!a.blocked} onClick={() => decide(true)}>
                  Aprovar e rodar
                </button>
                <button className="danger" disabled={busy} onClick={() => decide(false)}>
                  Recusar
                </button>
              </div>
            ) : (
              <p className="muted">Só o administrador aprova ou recusa uma tentativa.</p>
            )}
          </>
        )}
      </div>

      {a.cycle && <CycleCard a={a} />}

      <div className="card">
        <div className="card-head">
          <h2>Escopo do patch sugerido</h2>
          <span className="muted small">
            {a.review.files.length} arquivo(s), {a.review.lines} linha(s)
          </span>
        </div>
        {a.review.violations.length > 0 && (
          <ul className="error">
            {a.review.violations.map((v, i) => (
              <li key={i}>{v}</li>
            ))}
          </ul>
        )}
        {(a.highlights ?? a.review.highlights).map((h, i) => (
          <p key={i} className="notice">
            {h}
          </p>
        ))}
        <pre className="code">{a.proposed}</pre>
      </div>

      {a.rounds.map((r) => (
        <div className="card" key={r.n}>
          <div className="card-head">
            <h2>Rodada {r.n}</h2>
            <span className="muted small">
              {r.applied ? 'o patch aplicou como veio' : 'o patch não aplicou como veio'}
              {r.agentRan ? ` · o agente editou${r.costUsd ? ` (US$ ${r.costUsd.toFixed(2)})` : ''}` : ' · sem agente'}
              {' · '}
              {r.testExit === 0 ? 'build e testes passaram' : r.testExit < 0 ? 'build não rodou' : `build ou testes falharam (código ${r.testExit})`}
            </span>
          </div>
          {r.error && <p className="error">{r.error}</p>}
          {r.agentOutput && <p>{r.agentOutput}</p>}
          {r.agentError && <p className="error">{r.agentError}</p>}
          {r.review && r.review.violations.length > 0 && (
            <ul className="error">
              {r.review.violations.map((v, i) => (
                <li key={i}>{v}</li>
              ))}
            </ul>
          )}
          {r.diff && (
            <details open={r.testExit === 0}>
              <summary>Diff da rodada</summary>
              <pre className="code">{r.diff}</pre>
            </details>
          )}
          {r.testLog && (
            <details open={r.testExit > 0}>
              <summary>Fim do log do build e dos testes</summary>
              <pre className="code">{r.testLog}</pre>
            </details>
          )}
        </div>
      ))}
      {(a.status === 'queued' || a.status === 'running') && a.rounds.length === 0 && (
        <p className="muted">A primeira rodada está em andamento (baixar o código, aplicar o patch, build e testes). Costuma levar alguns minutos.</p>
      )}
    </section>
  );
}

const stageLabel: Record<FixCycle['stage'], string> = {
  building: 'Construindo as imagens de A e de B no CI do repositório',
  deploying: 'Subindo as versões A e B',
  testing: 'Rodando o experimento A/B',
  done: 'Resultado comentado no PR',
  failed: 'O re-teste não chegou a um resultado',
  skipped: 'Sem re-teste automático',
};

const outcomeLabel: Record<Outcome, string> = {
  melhorou: 'B melhorou',
  piorou: 'B piorou',
  sem_diferenca: 'Sem diferença',
  inconclusivo: 'Inconclusivo',
};
const outcomeClass: Record<Outcome, string> = { melhorou: 'resiliente', piorou: 'fragil', sem_diferenca: '', inconclusivo: 'atencao' };

// O que aconteceu depois do PR: a versão atual (A) contra a do PR (B).
function CycleCard({ a }: { a: FixAttempt }) {
  const c = a.cycle!;
  const running = c.stage === 'building' || c.stage === 'deploying' || c.stage === 'testing';
  return (
    <div className="card">
      <div className="card-head">
        <h2>Re-teste: versão atual contra a do PR</h2>
        <span className={`badge ${running ? 'running' : c.stage === 'done' ? 'completed' : c.stage === 'failed' ? 'failed' : ''}`}>{stageLabel[c.stage]}</span>
      </div>
      {c.outcome && (
        <p style={{ fontSize: 20, margin: '12px 0' }}>
          <span className={`grade-chip ${outcomeClass[c.outcome]}`}>{outcomeLabel[c.outcome]}</span>
        </p>
      )}
      {c.note && <p className={c.stage === 'failed' ? 'error' : 'muted'}>{c.note}</p>}
      <ul className="small">
        {c.imageA && (
          <li>
            A (commit testado): <code>{c.imageA}</code>
            {c.buildA?.url && (
              <>
                {' · '}
                <a href={c.buildA.url} target="_blank" rel="noreferrer">
                  build
                </a>
              </>
            )}
          </li>
        )}
        {c.imageB && (
          <li>
            B (branch do PR): <code>{c.imageB}</code>
            {c.buildB?.url && (
              <>
                {' · '}
                <a href={c.buildB.url} target="_blank" rel="noreferrer">
                  build
                </a>
              </>
            )}
          </li>
        )}
        {c.experimentId && (
          <li>
            <a href={`#/experimentos/${c.experimentId}`}>Experimento A/B</a>: a decisão é estatística, sem IA
          </li>
        )}
        {c.stage !== 'skipped' && <li>{c.commented ? 'Comentado no PR' : 'Ainda não comentado no PR'}</li>}
        {c.stage !== 'skipped' && !running && <li>{c.cleanedUp ? 'As cópias A e B foram apagadas do cluster' : 'Atenção: as cópias A e B não foram apagadas do cluster'}</li>}
      </ul>
    </div>
  );
}
