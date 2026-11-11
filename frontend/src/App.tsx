import { useEffect, useState } from 'react';
import { useRoute } from './router';
import { api, Health, Me, UNAUTHORIZED_EVENT } from './api';
import Icon, { IconName, Logo } from './components/Icon';
import Targets from './pages/Targets';
import Scenarios from './pages/Scenarios';
import ScenarioEditor from './pages/ScenarioEditor';
import Runs from './pages/Runs';
import RunView from './pages/RunView';
import Settings from './pages/Settings';
import Login from './pages/Login';
import Users from './pages/Users';

const nav: { path: string; label: string; icon: IconName }[] = [
  { path: 'alvos', label: 'Alvos', icon: 'target' },
  { path: 'cenarios', label: 'Cenários', icon: 'scenario' },
  { path: 'execucoes', label: 'Execuções', icon: 'pulse' },
  { path: 'configuracoes', label: 'Configurações', icon: 'settings' },
];

// Onde o backend está rodando: aparece no rodapé da barra lateral.
function Context({ h, down }: { h: Health | null; down: boolean }) {
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
  const [me, setMe] = useState<Me | null>(null);
  const [meError, setMeError] = useState(false);
  useEffect(() => {
    api
      .me()
      .then(setMe)
      .catch(() => setMeError(true));
    // Sessão expirada no meio do uso: volta para a tela de login.
    const out = () => setMe((m) => (m ? { authEnabled: m.authEnabled } : m));
    window.addEventListener(UNAUTHORIZED_EVENT, out);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, out);
  }, []);
  if (!me && !meError) return null;
  // Backend antigo ou fora do ar: segue sem login (o app mostra o erro em cada tela).
  if (me?.authEnabled && !me.user) return <Login onDone={setMe} />;
  return <Shell me={me} />;
}

function Shell({ me }: { me: Me | null }) {
  const [section = 'alvos', id] = useRoute();
  const [h, setH] = useState<Health | null>(null);
  const [down, setDown] = useState(false);
  useEffect(() => {
    api
      .health()
      .then(setH)
      .catch(() => setDown(true));
  }, []);
  const user = me?.user;
  const logout = async () => {
    await api.logout().catch(() => undefined);
    window.location.hash = '';
    window.location.reload();
  };
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
          {user?.admin && (
            <a href="#/usuarios" className={section === 'usuarios' ? 'active' : ''} aria-current={section === 'usuarios' ? 'page' : undefined}>
              <Icon name="users" />
              Usuários
            </a>
          )}
          {h?.grafanaUrl && (
            <a href={`${h.grafanaUrl}/`} target="_blank" rel="noreferrer" title="Grafana da stack de observabilidade (abre em outra aba)">
              <Icon name="chart" />
              Grafana
              <Icon name="external" size={14} className="trailing" />
            </a>
          )}
        </div>
        <Context h={h} down={down} />
        {user && (
          <div className="who">
            <span className="mono">{user.username}</span>
            <button className="link" onClick={logout} title="Sair">
              <Icon name="logout" size={16} />
              Sair
            </button>
          </div>
        )}
      </nav>
      <main>
        <div className="page">
          {section === 'alvos' && <Targets />}
          {section === 'cenarios' && !id && <Scenarios />}
          {section === 'cenarios' && id && <ScenarioEditor id={id === 'novo' ? undefined : id} />}
          {section === 'execucoes' && !id && <Runs />}
          {section === 'execucoes' && id && <RunView id={id} />}
          {section === 'configuracoes' && <Settings />}
          {section === 'usuarios' && user?.admin && <Users me={user} />}
        </div>
      </main>
    </div>
  );
}
