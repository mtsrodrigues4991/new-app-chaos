import { useEffect, useMemo, useState } from 'react';
import { Area, AreaChart, CartesianGrid, ResponsiveContainer, Tooltip, XAxis, YAxis } from 'recharts';
import { api, Container, defaultSLO, Endpoint, Fault, FaultType, Mode, ProfileType, Scenario, SLO, profileStages } from '../api';
import Timeline, { freeSlot } from '../components/Timeline';
import { faultMeta, faultTypes, newFaultId } from '../faults';
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

export const modeLabel: Record<Mode, string> = {
  load: 'Só carga',
  chaos: 'Só caos',
  both: 'Carga + caos',
};

const modeHelp: Record<Mode, string> = {
  load: 'Teste de carga com k6, sem falhas.',
  chaos: 'Injeta as falhas sem carga. Uma sonda chama um endpoint a cada 1 s para medir o impacto.',
  both: 'Carga do k6 com as falhas posicionadas sobre a curva de usuários.',
};

type Draft = Omit<Scenario, 'id'> & { id?: string };

const empty: Draft = {
  name: '',
  target: { container: '', baseUrl: '' },
  mode: 'load',
  endpoints: [{ method: 'GET', path: '/', weight: 1 }],
  profile: { type: 'plateau', vus: 20, durationSeconds: 60 },
  faults: [],
  probePath: '/health',
  slo: defaultSLO,
};

// Resumo de uma linha do cenário, para as listas.
export function scenarioSummary(s: Scenario): string {
  const mode = s.mode || 'load';
  const parts: string[] = [modeLabel[mode]];
  if (mode !== 'chaos') parts.push(`${profileLabel[s.profile.type]}, ${s.profile.vus} VUs`);
  const n = s.faults?.length ?? 0;
  if (mode !== 'load') parts.push(`${n} falha${n === 1 ? '' : 's'}`);
  parts.push(`${s.profile.durationSeconds} s`);
  return parts.join(' · ');
}

// Problemas que o backend também recusaria, mostrados antes de salvar.
function faultIssues(faults: Fault[], duration: number): string[] {
  const out: string[] = [];
  const sorted = [...faults].sort((a, b) => a.startSeconds - b.startSeconds);
  sorted.forEach((f, i) => {
    const name = `${faultMeta[f.type].label} em ${f.startSeconds}s`;
    if (f.startSeconds + f.durationSeconds > duration) out.push(`${name} termina depois do fim do teste (${duration}s).`);
    if (i > 0 && f.startSeconds < sorted[i - 1].startSeconds + sorted[i - 1].durationSeconds) out.push(`${name} se sobrepõe à falha anterior.`);
  });
  return out;
}

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
  const [selected, setSelected] = useState<string | null>(null);
  const [addError, setAddError] = useState<string | null>(null);

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
        .then((sc) => setDraft({ ...sc, mode: sc.mode || 'load', faults: sc.faults ?? [], probePath: sc.probePath ?? '/health', slo: sc.slo ?? defaultSLO, endpoints: sc.endpoints ?? empty.endpoints }))
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

  const faults = draft.faults ?? [];
  const setFaults = (fs: Fault[]) => set({ faults: fs });
  const sel = faults.find((f) => f.id === selected) ?? null;
  const setSel = (patch: Partial<Fault>) => setFaults(faults.map((f) => (f.id === selected ? { ...f, ...patch } : f)));
  const duration = draft.profile.durationSeconds;
  const issues = draft.mode === 'load' ? [] : faultIssues(faults, duration);

  const addFault = (type: FaultType) => {
    setAddError(null);
    const want = Math.max(1, Math.min(10, Math.floor(duration / 6)));
    const slot = freeSlot(faults, duration, want);
    if (!slot) {
      setAddError('Não há espaço livre na linha do tempo. Encurte ou remova uma falha, ou aumente a duração.');
      return;
    }
    const f: Fault = { id: newFaultId(), type, startSeconds: slot.start, durationSeconds: slot.dur, params: {} };
    setFaults([...faults, f]);
    setSelected(f.id);
  };

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
        <legend>O que rodar</legend>
        <div className="profile-types">
          {(Object.keys(modeLabel) as Mode[]).map((m) => (
            <label key={m} className={`choice ${draft.mode === m ? 'selected' : ''}`}>
              <input type="radio" name="mode" checked={draft.mode === m} onChange={() => set({ mode: m })} />
              <strong>{modeLabel[m]}</strong>
              <span className="muted">{modeHelp[m]}</span>
            </label>
          ))}
        </div>
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

      {draft.mode === 'chaos' && (
        <fieldset>
          <legend>Duração e sonda</legend>
          <div className="row">
            <label>
              Duração (segundos)
              <input
                type="number"
                min={10}
                max={3600}
                value={duration}
                onChange={(e) => set({ profile: { ...draft.profile, durationSeconds: Number(e.target.value) } })}
              />
            </label>
            <label className="grow">
              Endpoint da sonda (GET a cada 1 s)
              <input value={draft.probePath ?? ''} onChange={(e) => set({ probePath: e.target.value })} placeholder="/health" />
            </label>
          </div>
        </fieldset>
      )}

      {draft.mode !== 'chaos' && (
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
      )}

      {draft.mode !== 'chaos' && (
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
        {draft.mode === 'load' && (
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
        )}
      </fieldset>
      )}

      {draft.mode !== 'load' && (
        <fieldset className="faults">
          <legend>Falhas na linha do tempo</legend>
          <p className="muted small">
            Adicione uma falha e arraste o bloco para escolher quando ela acontece; puxe as bordas para mudar a duração. Neste
            marco, uma falha por vez. Toda falha é desfeita no fim do bloco, ao abortar ou se o backend reiniciar.
          </p>
          <div className="fault-add">
            {faultTypes.map((t) => (
              <button key={t} onClick={() => addFault(t)} style={{ ['--c' as string]: faultMeta[t].color }}>
                <span className="swatch" /> {faultMeta[t].label}
              </button>
            ))}
          </div>
          {addError && <p className="error">{addError}</p>}
          <Timeline
            duration={duration}
            faults={faults}
            selectedId={selected}
            onSelect={setSelected}
            onChange={setFaults}
            curve={draft.mode === 'both' ? preview : undefined}
          />
          {draft.mode === 'both' && <p className="muted small">A área clara atrás dos blocos é a curva de usuários virtuais do perfil de carga.</p>}
          {issues.map((i) => (
            <p key={i} className="error">
              {i}
            </p>
          ))}
          {sel ? (
            <div className="fault-form" style={{ ['--c' as string]: faultMeta[sel.type].color }}>
              <div className="row">
                <label>
                  Falha
                  <select value={sel.type} onChange={(e) => setSel({ type: e.target.value as FaultType, params: {} })}>
                    {faultTypes.map((t) => (
                      <option key={t} value={t}>
                        {faultMeta[t].label}
                      </option>
                    ))}
                  </select>
                </label>
                <label>
                  Início (s)
                  <input type="number" min={0} value={sel.startSeconds} onChange={(e) => setSel({ startSeconds: Math.max(0, Number(e.target.value)) })} />
                </label>
                <label>
                  Duração (s)
                  <input type="number" min={1} value={sel.durationSeconds} onChange={(e) => setSel({ durationSeconds: Math.max(1, Number(e.target.value)) })} />
                </label>
                {faultMeta[sel.type].params.map((p) => (
                  <label key={p.key} title={p.key}>
                    {p.label}
                    {p.unit ? ` (${p.unit})` : ''}
                    <input
                      type={p.text ? 'text' : 'number'}
                      min={p.min}
                      max={p.max}
                      value={sel.params[p.key] ?? p.def}
                      placeholder={p.help}
                      onChange={(e) => setSel({ params: { ...sel.params, [p.key]: e.target.value } })}
                    />
                  </label>
                ))}
              </div>
              <p className="muted small">
                {faultMeta[sel.type].help}
                {faultMeta[sel.type].params.some((p) => p.help) && ` Quantidade: ${faultMeta[sel.type].params.find((p) => p.help)?.help}.`}
              </p>
              <button className="link danger" onClick={() => { setFaults(faults.filter((f) => f.id !== sel.id)); setSelected(null); }}>
                Remover falha
              </button>
            </div>
          ) : (
            faults.length > 0 && <p className="muted small">Clique num bloco para editar os parâmetros.</p>
          )}
        </fieldset>
      )}

      <SLOFields draft={draft} set={set} />

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

// SLOs do veredito: cada segundo da execução fica dentro ou fora deles, e a nota sai daí.
function SLOFields({ draft, set }: { draft: Draft; set: (p: Partial<Draft>) => void }) {
  const slo = draft.slo ?? defaultSLO;
  const setSLO = (patch: Partial<SLO>) => set({ slo: { ...slo, ...patch } });
  const chaosOnly = draft.mode === 'chaos';
  return (
    <fieldset>
      <legend>Critérios de aprovação (SLOs)</legend>
      <p className="muted small">
        Cada segundo da execução fica dentro ou fora destes limites. A nota de resiliência (0 a 100) sai do tempo dentro deles durante cada falha e de quanto a
        aplicação demora para voltar depois. Os valores já vêm com uma sugestão.
      </p>
      <div className="row">
        <label>
          {chaosOnly ? 'Latência da sonda até (ms)' : 'p95 até (ms)'}
          <input type="number" min={1} value={slo.p95Ms} onChange={(e) => setSLO({ p95Ms: Number(e.target.value) })} />
        </label>
        {!chaosOnly && (
          <label>
            Taxa de erro até (%)
            <input type="number" min={0} max={100} step={0.1} value={slo.errorRatePct} onChange={(e) => setSLO({ errorRatePct: Number(e.target.value) })} />
          </label>
        )}
        {chaosOnly && (
          <label>
            Disponibilidade mínima (%)
            <input type="number" min={0} max={100} step={0.1} value={slo.availabilityPct} onChange={(e) => setSLO({ availabilityPct: Number(e.target.value) })} />
          </label>
        )}
        {!chaosOnly && (
          <label title="Uma requisição que passa deste tempo é cancelada e conta como erro">
            Timeout por requisição (s)
            <input
              type="number"
              min={1}
              max={120}
              value={draft.requestTimeoutSeconds ?? 10}
              onChange={(e) => set({ requestTimeoutSeconds: Number(e.target.value) })}
            />
          </label>
        )}
      </div>
      <button type="button" className="link" onClick={() => set({ slo: defaultSLO, requestTimeoutSeconds: undefined })}>
        Voltar aos valores sugeridos
      </button>
    </fieldset>
  );
}
