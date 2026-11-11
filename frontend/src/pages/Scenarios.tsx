import { useEffect, useState } from 'react';
import { api, Scenario } from '../api';
import { go } from '../router';
import { profileLabel } from './ScenarioEditor';

export default function Scenarios() {
  const [items, setItems] = useState<Scenario[]>([]);
  const [error, setError] = useState<string | null>(null);

  const load = () =>
    api
      .scenarios()
      .then(setItems)
      .catch((e: Error) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  const run = async (s: Scenario) => {
    try {
      const r = await api.startRun(s.id);
      go(`/execucoes/${r.id}`);
    } catch (e) {
      setError((e as Error).message);
    }
  };

  const remove = async (s: Scenario) => {
    if (!confirm(`Excluir o cenário "${s.name}"? As execuções já feitas continuam no histórico.`)) return;
    await api.deleteScenario(s.id).catch((e: Error) => setError(e.message));
    load();
  };

  return (
    <section>
      <div className="title-row">
        <h1>Cenários</h1>
        <a className="button primary" href="#/cenarios/novo">
          Novo cenário
        </a>
      </div>
      {error && <p className="error">Erro: {error}</p>}
      {items.length === 0 && !error && <p className="muted">Nenhum cenário ainda. Crie um a partir de um alvo.</p>}
      {items.length > 0 && (
        <table>
          <thead>
            <tr>
              <th>Nome</th>
              <th>Alvo</th>
              <th>Endpoints</th>
              <th>Carga</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {items.map((s) => (
              <tr key={s.id}>
                <td>
                  <a href={`#/cenarios/${s.id}`}>{s.name}</a>
                </td>
                <td>{s.target.baseUrl}</td>
                <td>{s.endpoints.length}</td>
                <td>
                  {profileLabel[s.profile.type]}, {s.profile.vus} VUs, {s.profile.durationSeconds} s
                </td>
                <td className="actions">
                  <button className="primary" onClick={() => run(s)}>
                    Rodar
                  </button>
                  <a href={`#/execucoes?cenario=${s.id}`}>Execuções</a>
                  <button className="link danger" onClick={() => remove(s)}>
                    Excluir
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
