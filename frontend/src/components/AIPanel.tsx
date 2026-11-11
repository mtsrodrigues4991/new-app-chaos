import { useEffect, useRef, useState } from 'react';
import { api, ChatMessage, Report, RunDetail } from '../api';
import { faultMeta, faultSummary } from '../faults';
import RichText from './RichText';

const regressionLabel = { melhorou: '↑ Melhorou', igual: '= Igual', piorou: '↓ Piorou' };

// Relatório da IA (sob demanda) e chat sobre a execução.
export default function AIPanel({ run, onReport }: { run: RunDetail; onReport: (r: Report) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const rep = run.analysis;
  const hasRef = !!run.referenceRunId && run.referenceRunId !== run.id;

  const analyze = async () => {
    setBusy(true);
    setError(null);
    try {
      onReport(await api.analyze(run.id));
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const faultName = (id: string) => {
    const f = run.scenario.faults?.find((x) => x.id === id);
    return f ? faultSummary(f) : id;
  };

  return (
    <section className="ai" id="analise">
      <div className="title-row">
        <h2>Análise da IA</h2>
        <button className={rep ? '' : 'primary'} disabled={busy} onClick={analyze}>
          {busy ? 'Analisando…' : rep ? 'Refazer análise' : 'Analisar com IA'}
        </button>
      </div>
      {busy && (
        <p className="muted">
          {run.scenario.code?.repo
            ? 'A IA está lendo as métricas e o código da aplicação. Costuma levar de 1 a 5 min.'
            : 'A IA está lendo as métricas da execução. Costuma levar de 30 s a 2 min.'}
        </p>
      )}
      {error && (
        <p className="error">
          {error}
          {error.includes('.env') && (
            <>
              {' '}
              (<a href="#/configuracoes">ver Configurações</a>)
            </>
          )}
        </p>
      )}
      {!rep && <p className="print-only">Ainda não há análise da IA para esta execução.</p>}
      {!rep && !busy && (
        <p className="muted no-print">
          A análise só roda quando você pede. Ela resume a execução, aponta o ponto de quebra, explica a nota e sugere causas e correções.
          {hasRef
            ? ' Como este cenário tem uma execução de referência, ela também compara as duas.'
            : ' Para ver regressões, marque uma execução deste cenário como referência.'}
        </p>
      )}
      {rep && (
        <div className="report">
          <p className="muted small">
            Gerada em {new Date(rep.createdAt).toLocaleString('pt-BR')} por {rep.usage.model} · {rep.usage.inputTokens.toLocaleString('pt-BR')} tokens de entrada,{' '}
            {rep.usage.outputTokens.toLocaleString('pt-BR')} de saída
            {rep.referenceRunId ? (
              <>
                {' '}
                · comparada com a <a href={`#/execucoes/${rep.referenceRunId}`}>execução de referência</a>
              </>
            ) : (
              ' · sem execução de referência'
            )}
          </p>
          {rep.code && (
            <details className="small muted code-read">
              <summary>
                Leu {rep.code.filesRead.length} {rep.code.filesRead.length === 1 ? 'arquivo' : 'arquivos'} de{' '}
                <a href={`https://github.com/${rep.code.repo}/tree/${rep.code.commit}${rep.code.path ? `/${rep.code.path}` : ''}`} target="_blank" rel="noreferrer">
                  {rep.code.repo}
                </a>{' '}
                ({rep.code.branch} @ <code>{rep.code.commit.slice(0, 7)}</code>)
              </summary>
              <ul>
                {rep.code.filesRead.map((f, i) => (
                  <li key={i}>
                    <code>{f}</code>
                  </li>
                ))}
              </ul>
            </details>
          )}
          <h3>Resumo</h3>
          <RichText text={rep.content.summary} />
          <h3>Por que a nota ficou assim</h3>
          <RichText text={rep.content.scoreExplanation} />
          <h3>Ponto de quebra</h3>
          <p>
            <span className={`pill ${rep.content.breakingPoint.found ? 'bad' : 'good'}`}>{rep.content.breakingPoint.found ? 'Encontrado' : 'Não quebrou'}</span>{' '}
            {rep.content.breakingPoint.description}
          </p>
          {rep.content.faultFindings.length > 0 && (
            <>
              <h3>Efeito de cada falha</h3>
              <ul>
                {rep.content.faultFindings.map((f) => {
                  const type = run.scenario.faults?.find((x) => x.id === f.faultId)?.type;
                  return (
                    <li key={f.faultId}>
                      {type && <span className="swatch" style={{ ['--c' as string]: faultMeta[type].color }} />} <strong>{faultName(f.faultId)}:</strong> {f.finding}
                    </li>
                  );
                })}
              </ul>
            </>
          )}
          {rep.content.regression && (
            <>
              <h3>Comparação com a referência</h3>
              <p>
                <span className={`pill ${rep.content.regression.verdict === 'piorou' ? 'bad' : rep.content.regression.verdict === 'melhorou' ? 'good' : ''}`}>
                  {regressionLabel[rep.content.regression.verdict]}
                </span>{' '}
                {rep.content.regression.summary}
              </p>
              {rep.content.regression.items.length > 0 && (
                <ul>
                  {rep.content.regression.items.map((it, i) => (
                    <li key={i}>{it}</li>
                  ))}
                </ul>
              )}
            </>
          )}
          <h3>Causas prováveis</h3>
          <ol>
            {rep.content.causes.map((c, i) => (
              <li key={i}>{c}</li>
            ))}
          </ol>
          <h3>Sugestões de correção</h3>
          <ol>
            {rep.content.fixes.map((f, i) => (
              <li key={i}>
                <strong>{f.title}.</strong> {f.detail}
                {f.file && (
                  <div className="fix-file small">
                    {f.url ? (
                      <a href={f.url} target="_blank" rel="noreferrer">
                        <code>
                          {f.file}
                          {f.line ? `:${f.line}` : ''}
                        </code>
                      </a>
                    ) : (
                      <code>{f.file}</code>
                    )}
                    {f.newFile && <span className="muted"> (arquivo novo)</span>}
                  </div>
                )}
                {f.code && <pre className="code">{f.code}</pre>}
              </li>
            ))}
          </ol>
        </div>
      )}
      <Chat runId={run.id} hasCode={!!run.scenario.code?.repo} />
    </section>
  );
}

const suggestions = ['Por que a nota ficou baixa?', 'O que eu deveria corrigir primeiro?', 'Como esta execução se compara com as anteriores deste cenário?'];
const codeSuggestions = ['Onde está configurado o timeout do banco?', 'Qual parte do código atende o endpoint mais lento?'];

function Chat({ runId, hasCode }: { runId: string; hasCode: boolean }) {
  const [msgs, setMsgs] = useState<ChatMessage[]>([]);
  const [text, setText] = useState('');
  const [pending, setPending] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const end = useRef<HTMLDivElement>(null);
  // Só rola até o fim depois que o usuário pergunta; ao abrir a página, fica no topo.
  const follow = useRef(false);

  useEffect(() => {
    api.chat(runId).then(setMsgs).catch((e: Error) => setError(e.message));
  }, [runId]);

  useEffect(() => {
    if (follow.current) end.current?.scrollIntoView({ block: 'nearest' });
  }, [msgs, pending]);

  const send = async (q: string) => {
    q = q.trim();
    if (!q || pending) return;
    follow.current = true;
    setPending(q);
    setText('');
    setError(null);
    try {
      const added = await api.ask(runId, q);
      setMsgs((m) => [...m, ...added]);
    } catch (e) {
      setError((e as Error).message);
      setText(q); // devolve a pergunta para tentar de novo
    } finally {
      setPending(null);
    }
  };

  const clear = async () => {
    if (!confirm('Apagar a conversa desta execução?')) return;
    await api.clearChat(runId).catch(() => undefined);
    setMsgs([]);
  };

  return (
    <div className="chat no-print">
      <div className="title-row">
        <h3>Conversar sobre esta execução</h3>
        {msgs.length > 0 && (
          <button className="link danger" onClick={clear}>
            Apagar conversa
          </button>
        )}
      </div>
      <p className="muted small">A IA vê esta execução, o relatório (se houver), a execução de referência, o histórico deste mesmo cenário e, se o cenário tiver repositório, o código da aplicação. A conversa fica salva aqui.</p>
      <div className="chat-log">
        {msgs.map((m) => (
          <div key={m.id} className={`msg ${m.role}`}>
            {m.role === 'assistant' ? <RichText text={m.content} /> : m.content}
          </div>
        ))}
        {pending && (
          <>
            <div className="msg user">{pending}</div>
            <div className="msg assistant muted">Pensando…</div>
          </>
        )}
        <div ref={end} />
      </div>
      {msgs.length === 0 && !pending && (
        <div className="suggestions">
          {[...suggestions, ...(hasCode ? codeSuggestions : [])].map((s) => (
            <button key={s} onClick={() => send(s)}>
              {s}
            </button>
          ))}
        </div>
      )}
      {error && <p className="error">{error}</p>}
      <form
        className="chat-input"
        onSubmit={(e) => {
          e.preventDefault();
          send(text);
        }}
      >
        <textarea
          rows={2}
          value={text}
          placeholder="Pergunte algo sobre o resultado…"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === 'Enter' && !e.shiftKey) {
              e.preventDefault();
              send(text);
            }
          }}
        />
        <button className="primary" disabled={!!pending || !text.trim()}>
          Enviar
        </button>
      </form>
    </div>
  );
}
