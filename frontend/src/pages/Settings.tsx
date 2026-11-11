import { useEffect, useState } from 'react';
import { AISettings, api } from '../api';

// Configurações da IA: só provedor e modelo. A chave da API fica no .env, nunca no banco.
export default function Settings() {
  const [s, setS] = useState<AISettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api.aiSettings().then(setS).catch((e: Error) => setError(e.message));
  }, []);

  if (error && !s) return <p className="error">Erro: {error}</p>;
  if (!s) return <p>Carregando…</p>;

  const provider = s.providers?.find((p) => p.id === s.provider) ?? s.providers?.[0];
  const known = provider?.models.some((m) => m.id === s.model);

  const save = async () => {
    setSaving(true);
    setError(null);
    setSaved(false);
    try {
      setS(await api.saveAISettings(s));
      setSaved(true);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  return (
    <section className="editor">
      <h1>Configurações</h1>
      <fieldset>
        <legend>Inteligência artificial</legend>
        <p className="muted small">
          Usada no botão “Analisar com IA” e no chat de cada execução. A análise só roda quando você pede; cada análise e cada pergunta consomem créditos da sua
          conta no provedor.
        </p>
        <div className="row">
          <label>
            Provedor
            <select
              value={s.provider}
              onChange={(e) => {
                const p = s.providers?.find((x) => x.id === e.target.value);
                setS({ ...s, provider: e.target.value, model: p?.defaultModel ?? s.model });
                setSaved(false);
              }}
            >
              {s.providers?.map((p) => (
                <option key={p.id} value={p.id}>
                  {p.label}
                </option>
              ))}
            </select>
          </label>
          <label className="grow">
            Modelo
            <select
              value={known ? s.model : '__outro'}
              onChange={(e) => {
                setS({ ...s, model: e.target.value === '__outro' ? '' : e.target.value });
                setSaved(false);
              }}
            >
              {provider?.models.map((m) => (
                <option key={m.id} value={m.id}>
                  {m.label}
                  {m.note ? ` (${m.note})` : ''}
                </option>
              ))}
              <option value="__outro">Outro (digitar o id)</option>
            </select>
          </label>
          {!known && (
            <label className="grow">
              Id do modelo
              <input value={s.model} placeholder={provider?.defaultModel} onChange={(e) => setS({ ...s, model: e.target.value })} />
            </label>
          )}
        </div>
        {provider &&
          (provider.configured ? (
            <p className="small ok-text">✓ Chave encontrada ({provider.keyEnv} no .env).</p>
          ) : (
            <div className="notice">
              <strong>Falta a chave da API.</strong> Crie um arquivo <code>.env</code> na pasta do projeto (ao lado do <code>docker-compose.yml</code>) com a
              linha <code>{provider.keyEnv}=sua-chave</code> e rode <code>docker compose up -d</code> de novo. A chave fica só no backend; o app nunca a salva no
              banco nem a mostra aqui.
            </div>
          ))}
      </fieldset>
      <fieldset>
        <legend>Código das aplicações (GitHub)</legend>
        <p className="muted small">
          Quando um cenário tem repositório, a IA lê o código na análise e no chat. Repositórios públicos funcionam sem token; os privados precisam de um token só de
          leitura.
        </p>
        {s.githubTokenConfigured ? (
          <p className="small ok-text">✓ Token encontrado (GITHUB_TOKEN no .env).</p>
        ) : (
          <p className="small muted">
            Sem token: só repositórios públicos. Para privados, crie no GitHub um token <em>fine-grained</em> com permissão <em>Contents: Read-only</em> nos
            repositórios desejados, adicione <code>GITHUB_TOKEN=...</code> no <code>.env</code> e rode <code>docker compose up -d</code>.
          </p>
        )}
      </fieldset>
      {error && <p className="error">Erro: {error}</p>}
      <div className="actions">
        <button className="primary" disabled={saving} onClick={save}>
          Salvar
        </button>
        {saved && <span className="muted">Salvo.</span>}
      </div>
    </section>
  );
}
