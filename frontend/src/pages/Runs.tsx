import { useEffect, useState } from 'react';
import { api, Run } from '../api';
import { query } from '../router';
import { scenarioSummary } from './ScenarioEditor';
import { StatusBadge, duration } from './RunView';

export default function Runs() {
  const scenarioId = query('cenario') ?? undefined;
  const [items, setItems] = useState<Run[]>([]);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const load = () =>
      api
        .runs(scenarioId)
        .then(setItems)
        .catch((e: Error) => setError(e.message));
    load();
    const t = setInterval(load, 3000); // mantém o estado das execuções em andamento atualizado
    return () => clearInterval(t);
  }, [scenarioId]);

  return (
    <section>
      <h1>Execuções</h1>
      {scenarioId && (
        <p className="muted">
          Filtrando por um cenário. <a href="#/execucoes">Ver todas</a>
        </p>
      )}
      {error && <p className="error">Erro: {error}</p>}
      {items.length === 0 && !error && <p className="muted">Nenhuma execução ainda. Rode um cenário.</p>}
      {items.length > 0 && (
        <div className="card table-card">
        <table>
          <thead>
            <tr>
              <th>Início</th>
              <th>Cenário</th>
              <th>Estado</th>
              <th>Duração</th>
              <th>Teste</th>
            </tr>
          </thead>
          <tbody>
            {items.map((r) => (
              <tr key={r.id}>
                <td>
                  <a href={`#/execucoes/${r.id}`}>{new Date(r.createdAt).toLocaleString('pt-BR')}</a>
                </td>
                <td>{r.scenarioName}</td>
                <td>
                  <StatusBadge status={r.status} />
                </td>
                <td>{duration(r)}</td>
                <td>{scenarioSummary(r.scenario)}</td>
              </tr>
            ))}
          </tbody>
        </table>
        </div>
      )}
    </section>
  );
}
