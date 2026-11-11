import { useEffect, useState } from 'react';
import { api, Container, DeploymentInfo, Health } from '../api';

// Alvos: containers do Docker local e/ou Deployments do cluster, conforme onde o backend roda.
export default function Targets() {
  const [health, setHealth] = useState<Health | null>(null);
  const [containers, setContainers] = useState<Container[]>([]);
  const [deployments, setDeployments] = useState<DeploymentInfo[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const load = async () => {
    setLoading(true);
    setError(null);
    try {
      const h = await api.health();
      setHealth(h);
      const [cs, ds] = await Promise.all([h.docker ? api.containers() : [], h.kubernetes ? api.deployments() : []]);
      setContainers(cs);
      setDeployments(ds);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    load();
  }, []);

  return (
    <section>
      <h1>Alvos</h1>
      <button onClick={load}>Atualizar</button>
      {loading && <p>Carregando…</p>}
      {error && <p className="error">Erro: {error}</p>}
      {!loading && health && !health.docker && !health.kubernetes && (
        <p className="error">O backend não alcança nem o Docker nem um cluster Kubernetes.</p>
      )}

      {!loading && health?.kubernetes && (
        <>
          <h2>Deployments do Kubernetes</h2>
          <p className="muted">
            Deployments do cluster que podem ser testados. As falhas são aplicadas pelo Chaos Mesh nos pods do Deployment e o k6 roda como um Job no
            cluster.
          </p>
          {deployments.length === 0 ? (
            <p className="muted">Nenhum Deployment fora dos namespaces do sistema.</p>
          ) : (
            <div className="card table-card">
            <table>
              <thead>
                <tr>
                  <th>Namespace</th>
                  <th>Deployment</th>
                  <th>Pods prontos</th>
                  <th>Imagem</th>
                  <th>URL (Service)</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {deployments.map((d) => (
                  <tr key={`${d.namespace}/${d.name}`}>
                    <td>{d.namespace}</td>
                    <td>{d.name}</td>
                    <td className={d.ready < d.replicas ? 'error' : undefined}>
                      {d.ready}/{d.replicas}
                    </td>
                    <td>{d.images.join(', ')}</td>
                    <td>{d.urls.join(', ') || <span className="muted">sem Service</span>}</td>
                    <td>
                      <a href={`#/cenarios/novo?deployment=${encodeURIComponent(`${d.namespace}/${d.name}`)}`}>Criar cenário</a>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
            </div>
          )}
        </>
      )}

      {!loading && health?.docker && (
        <>
          <h2>Containers do Docker</h2>
          <p className="muted">Containers Docker em execução que podem ser testados.</p>
          <div className="card table-card">
          <table>
            <thead>
              <tr>
                <th>Nome</th>
                <th>Imagem</th>
                <th>Estado</th>
                <th>Portas</th>
                <th>Rede</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {containers.map((c) => (
                <tr key={c.id}>
                  <td>{c.name}</td>
                  <td>{c.image}</td>
                  <td>{c.state}</td>
                  <td>{c.ports.join(', ')}</td>
                  <td>{c.networks.join(', ')}</td>
                  <td>
                    <a href={`#/cenarios/novo?container=${encodeURIComponent(c.name)}`}>Criar cenário</a>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
          </div>
        </>
      )}
    </section>
  );
}
