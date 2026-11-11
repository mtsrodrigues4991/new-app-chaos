import { useRoute } from './router';
import Targets from './pages/Targets';
import Scenarios from './pages/Scenarios';
import ScenarioEditor from './pages/ScenarioEditor';
import Runs from './pages/Runs';
import RunView from './pages/RunView';
import Settings from './pages/Settings';

const nav = [
  { path: 'alvos', label: 'Alvos' },
  { path: 'cenarios', label: 'Cenários' },
  { path: 'execucoes', label: 'Execuções' },
  { path: 'configuracoes', label: 'Configurações' },
];

export default function App() {
  const [section = 'alvos', id] = useRoute();
  return (
    <div className="layout">
      <nav>
        <strong>new-app-chaos</strong>
        {nav.map((n) => (
          <a key={n.path} href={`#/${n.path}`} className={n.path === section ? 'active' : ''}>
            {n.label}
          </a>
        ))}
      </nav>
      <main>
        {section === 'alvos' && <Targets />}
        {section === 'cenarios' && !id && <Scenarios />}
        {section === 'cenarios' && id && <ScenarioEditor id={id === 'novo' ? undefined : id} />}
        {section === 'execucoes' && !id && <Runs />}
        {section === 'execucoes' && id && <RunView id={id} />}
        {section === 'configuracoes' && <Settings />}
      </main>
    </div>
  );
}
