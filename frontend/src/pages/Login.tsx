import { FormEvent, useState } from 'react';
import { api, Me } from '../api';
import { Logo } from '../components/Icon';

// Tela de login do modo nuvem. Os usuários são criados pelo administrador.
export default function Login({ onDone }: { onDone: (me: Me) => void }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      onDone(await api.login(username, password));
    } catch (err) {
      setError((err as Error).message);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="login-page">
      <form className="card login-card editor" onSubmit={submit}>
        <div className="brand login-brand">
          <Logo />
          <span>new-app-chaos</span>
        </div>
        <p className="muted small">Carga, caos e IA no mesmo lugar. Entre com o usuário e a senha que você recebeu.</p>
        <label>
          Usuário
          <input autoFocus autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} required />
        </label>
        <label>
          Senha
          <input type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} required />
        </label>
        {error && <p className="error small">{error}</p>}
        <button className="primary" type="submit" disabled={busy || !username || !password}>
          {busy ? 'Entrando…' : 'Entrar'}
        </button>
      </form>
    </div>
  );
}
