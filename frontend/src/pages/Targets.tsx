import { useEffect, useState } from 'react';
import { Container, listContainers } from '../api';

export default function Targets() {
  const [items, setItems] = useState<Container[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = () => {
    setLoading(true);
    listContainers()
      .then((c) => {
        setItems(c);
        setError(null);
      })
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  };

  useEffect(load, []);

  return (
    <section>
      <h1>Alvos</h1>
      <p className="muted">Containers Docker em execução que podem ser testados.</p>
      <button onClick={load}>Atualizar</button>
      {loading && <p>Carregando…</p>}
      {error && <p className="error">Erro: {error}</p>}
      {!loading && !error && (
        <table>
          <thead>
            <tr>
              <th>Nome</th>
              <th>Imagem</th>
              <th>Estado</th>
              <th>Portas</th>
            </tr>
          </thead>
          <tbody>
            {items.map((c) => (
              <tr key={c.id}>
                <td>{c.name}</td>
                <td>{c.image}</td>
                <td>{c.state}</td>
                <td>{(c.ports ?? []).join(', ')}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
