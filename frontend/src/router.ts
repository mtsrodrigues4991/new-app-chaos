import { useEffect, useState } from 'react';

// Roteador mínimo por hash (#/cenarios/123), para não depender de biblioteca.
export function useRoute(): string[] {
  const parse = () => window.location.hash.replace(/^#\/?/, '').split('?')[0].split('/').filter(Boolean);
  const [parts, setParts] = useState(parse);
  useEffect(() => {
    const on = () => setParts(parse());
    window.addEventListener('hashchange', on);
    return () => window.removeEventListener('hashchange', on);
  }, []);
  return parts;
}

export function query(name: string): string | null {
  const q = window.location.hash.split('?')[1] ?? '';
  return new URLSearchParams(q).get(name);
}

export function go(path: string) {
  window.location.hash = path;
}
