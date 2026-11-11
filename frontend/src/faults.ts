// Metadados das falhas: nomes, cores e parâmetros (nomes iguais aos do krkn e do backend).
import { Fault, FaultType } from './api';

export type ParamDef = { key: string; label: string; unit?: string; def: string; min?: number; max?: number; text?: boolean; help?: string };

export const faultMeta: Record<FaultType, { label: string; short: string; color: string; help: string; params: ParamDef[] }> = {
  latency: {
    label: 'Latência',
    short: 'Latência',
    color: 'var(--fault-latency)',
    help: 'Atrasa os pacotes que saem do alvo (tc netem delay). As respostas da API chegam mais tarde.',
    params: [
      { key: 'latency', label: 'Atraso', unit: 'ms', def: '200', min: 1, max: 60000 },
      { key: 'jitter', label: 'Variação', unit: 'ms', def: '0', min: 0, max: 60000 },
    ],
  },
  packet_loss: {
    label: 'Perda de pacotes',
    short: 'Perda',
    color: 'var(--fault-loss)',
    help: 'Descarta uma parte dos pacotes que saem do alvo (tc netem loss). Gera retransmissões e timeouts.',
    params: [{ key: 'loss', label: 'Perda', unit: '%', def: '10', min: 1, max: 100 }],
  },
  cpu_stress: {
    label: 'Estresse de CPU',
    short: 'CPU',
    color: 'var(--fault-cpu)',
    help: 'Ocupa núcleos dentro do próprio container alvo, disputando CPU com a aplicação.',
    params: [
      { key: 'cpu-load-percentage', label: 'Carga por núcleo', unit: '%', def: '80', min: 1, max: 100 },
      { key: 'cpu-workers', label: 'Núcleos', def: '1', min: 1, max: 64 },
    ],
  },
  memory_stress: {
    label: 'Estresse de memória',
    short: 'Memória',
    color: 'var(--fault-mem)',
    help: 'Aloca memória dentro do container alvo. Perto do limite do container, pode levar a OOM.',
    params: [{ key: 'memory-vm-bytes', label: 'Quantidade', def: '256m', text: true, help: 'ex.: 256m, 1g ou 80% do que está livre' }],
  },
  kill_restart: {
    label: 'Kill/restart',
    short: 'Kill',
    color: 'var(--fault-kill)',
    help: 'Derruba o container no início do bloco e o sobe de novo no fim. A duração do bloco é o tempo fora do ar.',
    params: [],
  },
};

export const faultTypes = Object.keys(faultMeta) as FaultType[];

// Resumo curto dos parâmetros, para mostrar no bloco e nas faixas dos gráficos.
export function faultSummary(f: Fault): string {
  const m = faultMeta[f.type];
  const parts = m.params
    .map((p) => {
      const v = f.params[p.key] || p.def;
      if (p.key === 'jitter' && v === '0') return '';
      if (p.key === 'cpu-workers') return `${v} núcleo${v === '1' ? '' : 's'}`;
      return p.key === 'jitter' ? `±${v}${p.unit ?? ''}` : `${v}${p.unit ?? ''}`;
    })
    .filter(Boolean);
  return parts.length ? `${m.short} ${parts.join(' ')}` : m.short;
}

export function newFaultId(): string {
  return Math.random().toString(36).slice(2, 10);
}
