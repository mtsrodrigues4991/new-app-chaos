import { useEffect, useMemo, useState } from 'react';
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import { api, Container, Endpoint, ProfileType, Scenario, profileStages } from '../api';
import { go, query } from '../router';

export const profileLabel: Record<ProfileType, string> = {
  ramp: 'Rampa',
  plateau: 'Patamar',
  spike: 'Pico',
};

const profileHelp: Record<ProfileType, string> = {
  ramp: 'Sobe de 0 até o número de VUs ao longo de toda a duração. Bom para achar o ponto de quebra.',
  plateau: 'Sobe em 10% do tempo, mantém a carga e desce nos 10% finais. Bom para ver estabilidade.',
  spike: 'Carga base (1/5 dos VUs) com um pico até o máximo no meio. Bom para ver recuperação.',
};

const methods = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE'];

type Draft = Omit<Scenario, 'id'> & { id?: string };

const empty: Draft = {
  name: '',
  target: { container: '', baseUrl: '' },
  mode: 'load',
  endpoints: [{ method: 'GET', path: '/', weight: 1 }],
  profile: { type: 'plateau', vus: 20, durationSeconds: 60 },
};

// URL que o k6 usa para chegar ao alvo pela rede do Docker: nome do serviço (ou container) + primeira porta.
function suggestedUrl(c: Container): string {
  const host = c.service || c.name;
  const port = c.ports[0];
  return `http://${host}${port ? `:${port}` : ''}`;
}

export default function ScenarioEditor({ id }: { id?: string }) {
  const [draft, setDraft] = useState<Draft>(empty);
  const [containers, setContainers] = useState<Container[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    api
      .containers()
      .then((cs) => {
        setContainers(cs);
        const pre = query('container');
        if (!id && pre) {
          const c = cs.find((x) => x.name === pre);
          if (c) setDraft((d) => ({ ...d, name: d.name || `Carga em ${c.service || c.name}`, target: { container: c.name, baseUrl: suggestedUrl(c) } }));
        }
      })
      .catch((e: Error) => setError(e.message));
    if (id) {
      api
        .scenario(id)
        .then(setDraft)
        .catch((e: Error) => setError(e.message));
    }
  }, [id]);

  const set = (patch: Partial<Draft>) => setDraft((d) => ({ ...d, ...patch }));
  const setEndpoint = (i: number, patch: Partial<Endpoint>) =>
    set({ endpoints: draft.endpoints.map((e, j) => (j === i ? { ...e, ...patch } : e)) });

  const pickContainer = (name: string) => {
    const c = containers.find((x) => x.name === name);
    set({ target: { container: name, baseUrl: c ? suggestedUrl(c) : draft.target.baseUrl } });
  };

  const preview = useMemo(() => {
    const pts = [{ t: 0, vus: 0 }];
    let t = 0;
    for (const s of profileStages(draft.profile)) {
      t += s.durationSeconds;
      pts.push({ t, vus: s.target });
    }
    return pts;
  }, [draft.profile]);

  const save = async (andRun: boolean) => {
    setSaving(true);
    setError(null);
    try {
      const saved = draft.id ? await api.updateScenario(draft as Scenario) : await api.createScenario(draft);
      if (andRun) {
        const run = await api.startRun(saved.id);
        go(`/execucoes/${run.id}`);
      } else {
        go('/cenarios');
      }
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };

  const known = containers.some((c) => c.name === draft.target.container);

  return (
    <section className="editor">
      <h1>{id ? 'Editar cenário' : 'Novo cenário'}</h1>

      <fieldset>
        <legend>Cenário</legend>
        <label>
          Nome
          <input value={draft.name} onChange={(e) => set({ name: e.target.value })} placeholder="ex.: Catálogo em patamar" />
        </label>
      </fieldset>

      <fieldset>
        <legend>Alvo</legend>
        <div className="row">
          <label>
            Container
            <select value={draft.target.container} onChange={(e) => pickContainer(e.target.value)}>
              <option value="">Escolha…</option>
              {!known && draft.target.container && <option value={draft.target.container}>{draft.target.container} (parado)</option>}
              {containers.map((c) => (
                <option key={c.id} value={c.name}>
                  {c.name}
                </option>
              ))}
            </select>
          </label>
          <label className="grow">
            URL base (vista de dentro da rede do Docker)
            <input
              value={draft.target.baseUrl}
              onChange={(e) => set({ target: { ...draft.target, baseUrl: e.target.value } })}
              placeholder="http://demo-api:8080"
            />
          </label>
        </div>
      </fieldset>

      <fieldset>
        <legend>Endpoints</legend>
        <table className="endpoints">
          <thead>
            <tr>
              <th>Método</th>
              <th>Caminho</th>
              <th>Corpo (JSON, opcional)</th>
              <th title="Proporção de requisições entre os endpoints">Peso</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {draft.endpoints.map((e, i) => (
              <tr key={i}>
                <td>
                  <select value={e.method} onChange={(ev) => setEndpoint(i, { method: ev.target.value })}>
                    {methods.map((m) => (
                      <option key={m}>{m}</option>
                    ))}
                  </select>
                </td>
                <td>
                  <input value={e.path} onChange={(ev) => setEndpoint(i, { path: ev.target.value })} placeholder="/items" />
                </td>
                <td>
                  <input
                    value={e.body ?? ''}
                    disabled={e.method === 'GET'}
                    onChange={(ev) =>
                      setEndpoint(i, {
                        body: ev.target.value,
                        headers: ev.target.value ? { 'Content-Type': 'application/json' } : undefined,
                      })
                    }
                    placeholder={e.method === 'GET' ? '—' : '{"nome": "x"}'}
                  />
                </td>
                <td>
                  <input
                    type="number"
                    min={1}
                    className="narrow"
                    value={e.weight}
                    onChange={(ev) => setEndpoint(i, { weight: Math.max(1, Number(ev.target.value)) })}
                  />
                </td>
                <td>
                  <button
                    className="link danger"
                    disabled={draft.endpoints.length === 1}
                    onClick={() => set({ endpoints: draft.endpoints.filter((_, j) => j !== i) })}
                  >
                    Remover
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
        <button onClick={() => set({ endpoints: [...draft.endpoints, { method: 'GET', path: '/', weight: 1 }] })}>
          Adicionar endpoint
        </button>
      </fieldset>

      <fieldset>
        <legend>Perfil de carga</legend>
        <div className="profile-types">
          {(Object.keys(profileLabel) as ProfileType[]).map((t) => (
            <label key={t} className={`choice ${draft.profile.type === t ? 'selected' : ''}`}>
              <input
                type="radio"
                name="profile"
                checked={draft.profile.type === t}
                onChange={() => set({ profile: { ...draft.profile, type: t } })}
              />
              <strong>{profileLabel[t]}</strong>
              <span className="muted">{profileHelp[t]}</span>
            </label>
          ))}
        </div>
        <div className="row">
          <label>
            Usuários virtuais (pico)
            <input
              type="number"
              min={1}
              max={5000}
              value={draft.profile.vus}
              onChange={(e) => set({ profile: { ...draft.profile, vus: Number(e.target.value) } })}
            />
          </label>
          <label>
            Duração (segundos)
            <input
              type="number"
              min={10}
              max={3600}
              value={draft.profile.durationSeconds}
              onChange={(e) => set({ profile: { ...draft.profile, durationSeconds: Number(e.target.value) } })}
            />
          </label>
        </div>
        <div className="chart small">
          <div className="chart-title">Prévia: usuários virtuais ao longo do tempo</div>
          <ResponsiveContainer width="100%" height={160}>
            <AreaChart data={preview} margin={{ top: 8, right: 16, bottom: 0, left: 0 }}>
              <CartesianGrid stroke="var(--grid)" vertical={false} />
              <XAxis dataKey="t" type="number" domain={[0, 'dataMax']} unit=" s" stroke="var(--axis)" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
              <YAxis allowDecimals={false} width={40} stroke="var(--axis)" tick={{ fill: 'var(--text-secondary)', fontSize: 12 }} />
              <Tooltip
                contentStyle={{ background: 'var(--surface-2)', border: '1px solid var(--grid)' }}
                formatter={(v: number) => [`${v} VUs`, 'Usuários']}
                labelFormatter={(t: number) => `${t} s`}
              />
              <Area type="linear" dataKey="vus" stroke="var(--series-1)" strokeWidth={2} fill="var(--series-1)" fillOpacity={0.15} isAnimationActive={false} />
            </AreaChart>
          </ResponsiveContainer>
        </div>
      </fieldset>

      {error && <p className="error">Erro: {error}</p>}
      <div className="actions">
        <button className="primary" disabled={saving} onClick={() => save(true)}>
          Salvar e rodar
        </button>
        <button disabled={saving} onClick={() => save(false)}>
          Salvar
        </button>
        <a href="#/cenarios">Cancelar</a>
      </div>
    </section>
  );
}
