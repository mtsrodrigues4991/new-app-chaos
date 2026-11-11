import { useState } from 'react';
import Targets from './pages/Targets';

// As quatro telas previstas na especificação. Só "Alvos" está implementada no esqueleto.
const pages = [
  { id: 'targets', label: 'Alvos' },
  { id: 'scenario', label: 'Editor de cenário' },
  { id: 'run', label: 'Execução ao vivo' },
  { id: 'report', label: 'Relatório' },
] as const;

type PageId = (typeof pages)[number]['id'];

export default function App() {
  const [page, setPage] = useState<PageId>('targets');
  return (
    <div className="layout">
      <nav>
        <strong>new-app-chaos</strong>
        {pages.map((p) => (
          <button key={p.id} className={p.id === page ? 'active' : ''} onClick={() => setPage(p.id)}>
            {p.label}
          </button>
        ))}
      </nav>
      <main>
        {page === 'targets' && <Targets />}
        {page === 'scenario' && <Placeholder text="Linha do tempo com raias por tipo de falha e curva de carga (marcos 2 e 3)." />}
        {page === 'run' && <Placeholder text="Gráficos ao vivo de latência, RPS e erro via WebSocket (marco 2)." />}
        {page === 'report' && <Placeholder text="Veredito, análise da IA e chat (marco 4)." />}
      </main>
    </div>
  );
}

function Placeholder({ text }: { text: string }) {
  return <p className="muted">Em construção. {text}</p>;
}
