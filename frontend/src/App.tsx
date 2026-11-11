import { useEffect, useState } from 'react';
import { useRoute } from './router';
import { api, Health } from './api';
import Icon, { IconName, Logo } from './components/Icon';
import Targets from './pages/Targets';
import Scenarios from './pages/Scenarios';
import ScenarioEditor from './pages/ScenarioEditor';
import Runs from './pages/Runs';
import RunView from './pages/RunView';
import Settings from './pages/Settings';

const nav: { path: string; label: string; icon: IconName }[] = [
  { path: 'alvos', label: 'Alvos', icon: 'target' },
  { path: 'cenarios', label: 'Cenários', icon: 'scenario' },
  { path: 'execucoes', label: 'Execuções', icon: 'pulse' },
  { path: 'configuracoes', label: 'Configurações', icon: 'settings' },
];

// Onde o backend está rodando: aparece no rodapé da barra lateral.
function Context() {
  const [h, setH] = useState<Health | null>(null);
  const [down, setDown] = useState(false);
  useEffect(() => {
    api
      .health()
      .then(setH)
      .catch(() => setDown(true));
  }, []);
  if (!h && !down) return null;
  const where = !h ? 'Backend fora do ar' : h.kubernetes ? 'Kubernetes' : h.docker ? 'Docker' : 'Sem Docker nem Kubernetes';
  const extra = h?.kubernetes ? ['Chaos Mesh', h.observability ? 'observabilidade ligada' : ''].filter(Boolean).join(' · ') : '';
  return (
    <div className="context">
      <div className="context-label">Ambiente</div>
      <div className="context-where">
        <span className={`dot ${h && (h.kubernetes || h.docker) ? 'ok' : 'bad'}`} />
        {where}
        {h?.namespace && <span className="mono"> · {h.namespace}</span>}
      </div>
      {extra && <div className="context-extra">{extra}</div>}
    </div>
  );
}

export default function App() {
  const [section = 'alvos', id] = useRoute();
  return (
    <div className="layout">
      <nav className="sidebar" aria-label="Principal">
        <a className="brand" href="#/execucoes">
          <Logo />
          <span>new-app-chaos</span>
        </a>
        <div className="nav-links">
          {nav.map((n) => (
            <a key={n.path} href={`#/${n.path}`} className={n.path === section ? 'active' : ''} aria-current={n.path === section ? 'page' : undefined}>
              <Icon name={n.icon} />
              {n.label}
            </a>
          ))}
        </div>
        <Context />
      </nav>
      <main>
        <div className="page">
          {section === 'alvos' && <Targets />}
          {section === 'cenarios' && !id && <Scenarios />}
          {section === 'cenarios' && id && <ScenarioEditor id={id === 'novo' ? undefined : id} />}
          {section === 'execucoes' && !id && <Runs />}
          {section === 'execucoes' && id && <RunView id={id} />}
          {section === 'configuracoes' && <Settings />}
        </div>
      </main>
    </div>
  );
}
