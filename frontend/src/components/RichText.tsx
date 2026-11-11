import { Fragment, ReactNode } from 'react';

// Formatação mínima para textos da IA: parágrafos, listas com "-" ou "1.", **negrito**, `código`,
// blocos ``` e links [texto](https://...). Não interpreta HTML (tudo vira nós de texto do React).
function inline(text: string): ReactNode[] {
  return text.split(/(\*\*[^*]+\*\*|`[^`]+`|\[[^\]]+\]\(https?:\/\/[^)\s]+\))/g).map((part, i) => {
    if (part.startsWith('**') && part.endsWith('**') && part.length > 4) return <strong key={i}>{part.slice(2, -2)}</strong>;
    if (part.startsWith('`') && part.endsWith('`') && part.length > 2) return <code key={i}>{part.slice(1, -1)}</code>;
    const link = /^\[([^\]]+)\]\((https?:\/\/[^)\s]+)\)$/.exec(part);
    if (link)
      return (
        <a key={i} href={link[2]} target="_blank" rel="noreferrer">
          {link[1]}
        </a>
      );
    return <Fragment key={i}>{part}</Fragment>;
  });
}

function blocks(text: string): ReactNode[] {
  const lines = text.trim().split('\n');
  const out: ReactNode[] = [];
  let para: string[] = [];
  const flush = () => {
    if (para.length) out.push(...paragraphs(para.join('\n'), out.length));
    para = [];
  };
  for (let i = 0; i < lines.length; i++) {
    if (lines[i].trim().startsWith('```')) {
      flush();
      const code: string[] = [];
      for (i++; i < lines.length && !lines[i].trim().startsWith('```'); i++) code.push(lines[i]);
      out.push(
        <pre key={`c${out.length}`} className="code">
          {code.join('\n')}
        </pre>,
      );
    } else para.push(lines[i]);
  }
  flush();
  return out;
}

function paragraphs(text: string, base: number): ReactNode[] {
  return text
    .split(/\n\s*\n/)
    .filter((b) => b.trim())
    .map((b, n) => {
      const i = `${base}-${n}`;
      const lines = b.split('\n').filter((l) => l.trim());
      const bullet = /^\s*[-*•]\s+/;
      const numbered = /^\s*\d+[.)]\s+/;
      if (lines.length && lines.every((l) => bullet.test(l))) {
        return (
          <ul key={i}>
            {lines.map((l, j) => (
              <li key={j}>{inline(l.replace(bullet, ''))}</li>
            ))}
          </ul>
        );
      }
      if (lines.length && lines.every((l) => numbered.test(l))) {
        return (
          <ol key={i}>
            {lines.map((l, j) => (
              <li key={j}>{inline(l.replace(numbered, ''))}</li>
            ))}
          </ol>
        );
      }
      return (
        <p key={i}>
          {lines.map((l, j) => (
            <Fragment key={j}>
              {j > 0 && <br />}
              {inline(l)}
            </Fragment>
          ))}
        </p>
      );
    });
}

export default function RichText({ text }: { text: string }) {
  return <>{blocks(text)}</>;
}
