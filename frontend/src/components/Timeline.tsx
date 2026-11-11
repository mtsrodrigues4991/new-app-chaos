import { KeyboardEvent, PointerEvent, useRef } from 'react';
import { Fault } from '../api';
import { faultMeta, faultSummary } from '../faults';

type Props = {
  duration: number;
  faults: Fault[];
  selectedId: string | null;
  onSelect: (id: string | null) => void;
  onChange: (faults: Fault[]) => void;
  // Curva de usuários virtuais desenhada atrás dos blocos (modo carga + caos).
  curve?: { t: number; vus: number }[];
};

type Drag = { id: string; mode: 'move' | 'left' | 'right'; x0: number; orig: Fault; lo: number; hi: number; width: number };

// Limites livres em volta de uma falha: fim da anterior e início da próxima (falhas não se sobrepõem).
export function neighbors(faults: Fault[], f: Fault, duration: number): { lo: number; hi: number } {
  let lo = 0;
  let hi = duration;
  for (const o of faults) {
    if (o.id === f.id) continue;
    const end = o.startSeconds + o.durationSeconds;
    if (end <= f.startSeconds) lo = Math.max(lo, end);
    else if (o.startSeconds >= f.startSeconds + f.durationSeconds) hi = Math.min(hi, o.startSeconds);
  }
  return { lo, hi };
}

// Primeiro espaço livre para uma falha nova (até `want` segundos). Deixa uma folga antes
// do bloco (tempo de "linha de base" para comparar), e só cola nos vizinhos se não houver outro jeito.
export function freeSlot(faults: Fault[], duration: number, want: number): { start: number; dur: number } | null {
  const pad = Math.max(1, Math.round(duration * 0.05));
  const sorted = [...faults].sort((a, b) => a.startSeconds - b.startSeconds);
  const gaps: { from: number; to: number }[] = [];
  let cursor = 0;
  for (const f of sorted) {
    if (f.startSeconds > cursor) gaps.push({ from: cursor, to: f.startSeconds });
    cursor = Math.max(cursor, f.startSeconds + f.durationSeconds);
  }
  if (duration > cursor) gaps.push({ from: cursor, to: duration });
  for (const g of gaps) if (g.to - g.from >= want + pad) return { start: g.from + pad, dur: want };
  for (const g of gaps) if (g.to - g.from >= want) return { start: g.from, dur: want };
  const best = gaps.sort((a, b) => b.to - b.from - (a.to - a.from))[0];
  return best ? { start: best.from, dur: best.to - best.from } : null;
}

function tickStep(duration: number): number {
  for (const s of [5, 10, 15, 30, 60, 120, 300, 600]) if (duration / s <= 10) return s;
  return 900;
}

const clamp = (v: number, lo: number, hi: number) => Math.max(lo, Math.min(hi, v));

export default function Timeline({ duration, faults, selectedId, onSelect, onChange, curve }: Props) {
  const track = useRef<HTMLDivElement>(null);
  const drag = useRef<Drag | null>(null);
  const d = Math.max(1, duration);

  const update = (id: string, start: number, dur: number) =>
    onChange(faults.map((f) => (f.id === id ? { ...f, startSeconds: start, durationSeconds: dur } : f)));

  const begin = (e: PointerEvent, f: Fault, mode: Drag['mode']) => {
    e.stopPropagation();
    e.preventDefault();
    onSelect(f.id);
    const { lo, hi } = neighbors(faults, f, d);
    drag.current = { id: f.id, mode, x0: e.clientX, orig: f, lo, hi, width: track.current?.getBoundingClientRect().width ?? 1 };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  };

  const move = (e: PointerEvent) => {
    const g = drag.current;
    if (!g) return;
    const dx = Math.round(((e.clientX - g.x0) / g.width) * d);
    const { startSeconds: s, durationSeconds: len } = g.orig;
    const end = s + len;
    if (g.mode === 'move') {
      update(g.id, clamp(s + dx, g.lo, g.hi - len), len);
    } else if (g.mode === 'left') {
      const ns = clamp(s + dx, g.lo, end - 1);
      update(g.id, ns, end - ns);
    } else {
      const ne = clamp(end + dx, s + 1, g.hi);
      update(g.id, s, ne - s);
    }
  };

  const end = () => {
    drag.current = null;
  };

  // Teclado: setas movem 1 s; Shift + setas mudam a duração.
  const onKey = (e: KeyboardEvent, f: Fault) => {
    const step = e.key === 'ArrowLeft' ? -1 : e.key === 'ArrowRight' ? 1 : 0;
    if (!step) return;
    e.preventDefault();
    const { lo, hi } = neighbors(faults, f, d);
    if (e.shiftKey) update(f.id, f.startSeconds, clamp(f.durationSeconds + step, 1, hi - f.startSeconds));
    else update(f.id, clamp(f.startSeconds + step, lo, hi - f.durationSeconds), f.durationSeconds);
  };

  const pct = (t: number) => `${(clamp(t, 0, d) / d) * 100}%`;
  const step = tickStep(d);
  const ticks: number[] = [];
  for (let t = 0; t <= d; t += step) ticks.push(t);

  const maxVus = curve ? Math.max(1, ...curve.map((p) => p.vus)) : 1;
  const path = curve?.map((p, i) => `${i ? 'L' : 'M'}${(p.t / d) * 1000},${100 - (p.vus / maxVus) * 90}`).join(' ');

  return (
    <div className="timeline">
      <div className={`timeline-track ${curve ? 'with-curve' : ''}`} ref={track} onPointerDown={() => onSelect(null)}>
        {ticks.map((t) => (
          <div key={t} className="timeline-grid" style={{ left: pct(t) }} />
        ))}
        {path && (
          <svg className="timeline-curve" viewBox="0 0 1000 100" preserveAspectRatio="none" aria-hidden>
            <path d={`${path} L1000,100 L0,100 Z`} className="area" />
            <path d={path} className="line" vectorEffect="non-scaling-stroke" />
          </svg>
        )}
        {faults.map((f) => {
          const m = faultMeta[f.type];
          const outside = f.startSeconds + f.durationSeconds > d;
          return (
            <div
              key={f.id}
              role="button"
              tabIndex={0}
              aria-label={`${m.label}, de ${f.startSeconds} a ${f.startSeconds + f.durationSeconds} segundos`}
              title={`${faultSummary(f)} · ${f.startSeconds}s → ${f.startSeconds + f.durationSeconds}s (arraste para mover, puxe as bordas para mudar a duração)`}
              className={`fault-block ${selectedId === f.id ? 'selected' : ''} ${outside ? 'invalid' : ''}`}
              style={{ left: pct(f.startSeconds), width: `calc(${pct(f.startSeconds + f.durationSeconds)} - ${pct(f.startSeconds)})`, ['--c' as string]: m.color }}
              onPointerDown={(e) => begin(e, f, 'move')}
              onPointerMove={move}
              onPointerUp={end}
              onPointerCancel={end}
              onKeyDown={(e) => onKey(e, f)}
              onFocus={() => onSelect(f.id)}
            >
              <span className="handle left" onPointerDown={(e) => begin(e, f, 'left')} onPointerMove={move} onPointerUp={end} />
              <span className="fault-label">{faultSummary(f)}</span>
              <span className="handle right" onPointerDown={(e) => begin(e, f, 'right')} onPointerMove={move} onPointerUp={end} />
            </div>
          );
        })}
      </div>
      <div className="timeline-axis">
        {ticks.map((t) => (
          <span key={t} style={{ left: pct(t) }}>
            {t}s
          </span>
        ))}
      </div>
    </div>
  );
}
