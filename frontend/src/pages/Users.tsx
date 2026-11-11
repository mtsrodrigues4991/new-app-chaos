import { FormEvent, useEffect, useState } from 'react';
import { api, User } from '../api';

// Usuários do modo nuvem (só o administrador vê). Quem entra pode fazer tudo no app.
export default function Users({ me }: { me: User }) {
  const [list, setList] = useState<User[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [admin, setAdmin] = useState(false);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState<string | null>(null);

  const load = () => api.users().then(setList).catch((e: Error) => setError(e.message));
  useEffect(() => {
    load();
  }, []);

  const create = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setCreated(null);
    try {
      const u = await api.createUser(username, password, admin);
      setCreated(u.username);
      setUsername('');
      setPassword('');
      setAdmin(false);
      load();
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  const remove = async (name: string) => {
    if (!confirm(`Apagar o usuário ${name}? Ele perde o acesso na hora.`)) return;
    try {
      await api.deleteUser(name);
      load();
    } catch (err) {
      setError((err as Error).message);
    }
  };

  return (
    <section className="editor">
      <h1>Usuários</h1>
      <p className="muted small">
        Quem tem usuário pode fazer tudo: criar cenários, rodar carga, injetar falhas e usar a IA. Usuários criados aqui somem quando o ambiente é desligado;
        os fixos ficam no segredo <span className="mono">APP_USERS</span>.
      </p>
      <form onSubmit={create}>
        <fieldset>
          <legend>Novo usuário</legend>
          <div className="row">
            <label className="grow">
              Usuário
              <input value={username} onChange={(e) => setUsername(e.target.value.toLowerCase())} placeholder="ex.: ana.silva" autoComplete="off" required />
            </label>
            <label className="grow">
              Senha (mínimo 8 caracteres)
              <input type="text" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" minLength={8} required />
            </label>
          </div>
          <label className="inline">
            <input type="checkbox" checked={admin} onChange={(e) => setAdmin(e.target.checked)} /> Administrador (também gerencia usuários)
          </label>
          <div className="actions">
            <button className="primary" type="submit" disabled={busy}>
              Criar usuário
            </button>
            {created && <span className="ok-text small">Usuário {created} criado. Mande a senha para a pessoa por um canal privado.</span>}
          </div>
        </fieldset>
      </form>
      {error && <p className="error">{error}</p>}
      <div className="card table-card">
        <table>
          <thead>
            <tr>
              <th>Usuário</th>
              <th>Papel</th>
              <th>Criado em</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {list?.map((u) => (
              <tr key={u.username}>
                <td className="mono">{u.username}</td>
                <td>{u.admin ? 'Administrador' : 'Convidado'}</td>
                <td>{new Date(u.createdAt).toLocaleString('pt-BR')}</td>
                <td>
                  {u.username !== me.username && (
                    <button className="link danger" onClick={() => remove(u.username)}>
                      Apagar
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
