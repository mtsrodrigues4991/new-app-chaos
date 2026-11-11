export type Container = {
  id: string;
  name: string;
  image: string;
  state: string;
  ports: number[] | null;
};

export async function listContainers(): Promise<Container[]> {
  const res = await fetch('/api/containers');
  if (!res.ok) {
    const body = await res.json().catch(() => ({}));
    throw new Error(body.error ?? `HTTP ${res.status}`);
  }
  return res.json();
}
