// Metadados das falhas: nomes, cores e parâmetros (nomes iguais aos do krkn e do backend).
import { Fault, FaultType } from './api';

export type ParamDef = { key: string; label: string; unit?: string; def: string; min?: number; max?: number; text?: boolean; help?: string };

export type FaultMeta = {
  label: string;
  short: string;
  color: string;
  help: string;
  k8sHelp: string;
  params: ParamDef[];
  k8sOnly?: boolean; // só existe com o Chaos Mesh
  network?: boolean; // pode ficar restrita ao caminho até uma dependência
};

// Cores por efeito: os tipos novos reaproveitam a cor da família (atraso, erro), e o rótulo do bloco diz qual é.
export const faultMeta: Record<FaultType, FaultMeta> = {
  latency: {
    label: 'Latência',
    short: 'Latência',
    color: 'var(--fault-latency)',
    network: true,
    help: 'Atrasa os pacotes que saem do alvo (tc netem delay). As respostas da API chegam mais tarde.',
    k8sHelp: 'Atrasa os pacotes que saem dos pods (NetworkChaos delay do Chaos Mesh). As respostas da API chegam mais tarde.',
    params: [
      { key: 'latency', label: 'Atraso', unit: 'ms', def: '200', min: 1, max: 60000 },
      { key: 'jitter', label: 'Variação', unit: 'ms', def: '0', min: 0, max: 60000 },
    ],
  },
  packet_loss: {
    label: 'Perda de pacotes',
    short: 'Perda',
    color: 'var(--fault-loss)',
    network: true,
    help: 'Descarta uma parte dos pacotes que saem do alvo (tc netem loss). Gera retransmissões e timeouts.',
    k8sHelp: 'Descarta uma parte dos pacotes que saem dos pods (NetworkChaos loss do Chaos Mesh). Gera retransmissões e timeouts.',
    params: [{ key: 'loss', label: 'Perda', unit: '%', def: '10', min: 1, max: 100 }],
  },
  cpu_stress: {
    label: 'Estresse de CPU',
    short: 'CPU',
    color: 'var(--fault-cpu)',
    help: 'Ocupa núcleos dentro do próprio container alvo, disputando CPU com a aplicação.',
    k8sHelp: 'Ocupa núcleos dentro dos pods (StressChaos do Chaos Mesh), disputando CPU com a aplicação e com o limite do pod.',
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
    k8sHelp: 'Aloca memória dentro dos pods (StressChaos do Chaos Mesh). Perto do limite do pod, o kubelet mata o container (OOMKilled).',
    params: [{ key: 'memory-vm-bytes', label: 'Quantidade', def: '256m', text: true, help: 'ex.: 256m, 1g ou 80% do que está livre' }],
  },
  kill_restart: {
    label: 'Kill/restart',
    short: 'Kill',
    color: 'var(--fault-kill)',
    help: 'Derruba o container no início do bloco e o sobe de novo no fim. A duração do bloco é o tempo fora do ar.',
    k8sHelp: 'Apaga o(s) pod(s) no início do bloco (PodChaos pod-kill); o Deployment recria. O bloco é o tempo em que a recuperação é observada.',
    params: [],
  },
  io_latency: {
    label: 'Atraso de disco',
    short: 'IO lento',
    color: 'var(--fault-latency)',
    k8sOnly: true,
    help: '',
    k8sHelp: 'Atrasa leituras e escritas num volume do container (IOChaos latency). Bom para simular disco lento num banco.',
    params: [
      { key: 'volume-path', label: 'Volume', def: '', text: true, help: 'ponto de montagem, ex.: /var/lib/postgresql/data' },
      { key: 'delay', label: 'Atraso', unit: 'ms', def: '100', min: 1, max: 60000 },
      { key: 'percent', label: 'Operações', unit: '%', def: '100', min: 1, max: 100 },
    ],
  },
  io_fault: {
    label: 'Erro de disco',
    short: 'IO erro',
    color: 'var(--fault-loss)',
    k8sOnly: true,
    help: '',
    k8sHelp: 'Faz parte das operações num volume do container falhar com um erro do sistema (IOChaos fault; 5 = EIO, erro de entrada/saída).',
    params: [
      { key: 'volume-path', label: 'Volume', def: '', text: true, help: 'ponto de montagem, ex.: /var/lib/postgresql/data' },
      { key: 'errno', label: 'Erro (errno)', def: '5', min: 1, max: 255 },
      { key: 'percent', label: 'Operações', unit: '%', def: '50', min: 1, max: 100 },
    ],
  },
  dns_error: {
    label: 'Erro de DNS',
    short: 'DNS',
    color: 'var(--fault-loss)',
    k8sOnly: true,
    help: '',
    k8sHelp: 'A resolução de nomes dos pods falha (DNSChaos error). Com o alvo, ele deixa de achar as dependências pelo nome do Service.',
    params: [{ key: 'patterns', label: 'Domínios', def: '', text: true, help: 'ex.: pix-postgres.* (vazio = todos)' }],
  },
  http_delay: {
    label: 'Atraso HTTP',
    short: 'HTTP lento',
    color: 'var(--fault-latency)',
    k8sOnly: true,
    help: '',
    k8sHelp: 'Atrasa as requisições HTTP que chegam aos pods, só no caminho e método escolhidos (HTTPChaos delay). Não vale para HTTPS.',
    params: [
      { key: 'delay', label: 'Atraso', unit: 'ms', def: '500', min: 1, max: 60000 },
      { key: 'port', label: 'Porta', def: '8080', min: 1, max: 65535 },
      { key: 'path', label: 'Caminho', def: '*', text: true },
      { key: 'method', label: 'Método', def: '', text: true },
    ],
  },
  http_abort: {
    label: 'Aborto HTTP',
    short: 'HTTP abortado',
    color: 'var(--fault-loss)',
    k8sOnly: true,
    help: '',
    k8sHelp: 'Corta a conexão das requisições HTTP que chegam aos pods, só no caminho e método escolhidos (HTTPChaos abort). Não vale para HTTPS.',
    params: [
      { key: 'port', label: 'Porta', def: '8080', min: 1, max: 65535 },
      { key: 'path', label: 'Caminho', def: '*', text: true },
      { key: 'method', label: 'Método', def: '', text: true },
    ],
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
      if (['volume-path', 'port', 'patterns', 'errno'].includes(p.key)) return '';
      if (p.key === 'percent') return v === '100' ? '' : `${v}%`;
      if (p.key === 'path') return v === '*' ? '' : v;
      if (p.key === 'method') return v.toUpperCase();
      if (p.key === 'cpu-workers') return `${v} núcleo${v === '1' ? '' : 's'}`;
      return p.key === 'jitter' ? `±${v}${p.unit ?? ''}` : `${v}${p.unit ?? ''}`;
    })
    .filter(Boolean);
  const base = parts.length ? `${m.short} ${parts.join(' ')}` : m.short;
  if (!f.dependency) return base;
  return `${base} ${f.linkOnly ? '→' : '@'} ${f.dependency.deployment}`;
}

export function newFaultId(): string {
  return Math.random().toString(36).slice(2, 10);
}
