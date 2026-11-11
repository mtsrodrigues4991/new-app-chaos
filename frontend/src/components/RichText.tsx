import { Fragment, ReactNode } from 'react';

// Formatação mínima para textos da IA: parágrafos, listas com "-" ou "1." e **negrito**.
// Não interpreta HTML (o texto vira nós de texto do React, sem dangerouslySetInnerHTML).
function inline(text: string): ReactNode[] {
  return text.split(/(\*\*[^*]+\*\*|`[^`]+`)/g).map((part, i) => {
    if (part.startsWith('**') && part.endsWith('**') && part.length > 4) return <strong key={i}>{part.slice(2, -2)}</strong>;
    if (part.startsWith('`') && part.endsWith('`') && part.length > 2) return <code key={i}>{part.slice(1, -1)}</code>;
    return <Fragment key={i}>{part}</Fragment>;
  });
}

export default function RichText({ text }: { text: string }) {
  const blocks = text.trim().split(/\n\s*\n/);
  return (
    <>
      {blocks.map((b, i) => {
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
      })}
    </>
  );
}
