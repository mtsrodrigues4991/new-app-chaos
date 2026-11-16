# Fase 1, comparar antes e depois: prova do critério de pronto

Data: 16/11/2026 · Código: Fase 1 de [analise-evolucao.md](../analise-evolucao.md), sobre o commit `2d954c3`

## Resultado

**Os dois critérios de pronto foram atendidos.**

| Experimento | Esperado | Decisão | p95 de A | p95 de B | Diferença | Intervalo de 95% |
|---|---|---|---|---|---|---|
| A/A: a mesma versão dos dois lados | sem diferença | **sem diferença** | 4,93 ms | 4,96 ms | +0,03 ms (+0,6%) | −0,09 a +0,26 ms |
| A/B: B com 20 ms de atraso artificial | piorou | **piorou** | 4,97 ms | 25,49 ms | +20,52 ms (+412,9%) | +20,26 a +20,89 ms |

Os dois decidiram no mínimo de execuções: 5 válidas por lado, 10 no total, nenhuma inválida.

## O que foi rodado

Os experimentos foram criados pela API, como a tela faz, num ambiente Docker próprio (rede isolada, sem
tocar no KIND nem na Azure):

- **Alvo A:** `demo-api`, limitado a 2 CPUs.
- **Alvo B:** a mesma imagem, com `DELAY_MS=20` (atraso fixo em `/items` e `/compute`), também com 2 CPUs.
- **Cenário:** perfil patamar de 40 s, taxa de chegada de 200 req/s, 100 VUs pré-alocados, 10 s de
  aquecimento, 90% `GET /items` e 10% `GET /compute`.
- **Métrica principal:** p95 da janela estável. Limite prático de 5% (com piso de 0,5 ms).
- **Ordem das execuções:** A B, B A, A B, B A, A B, com 5 s de pausa entre elas.

Para repetir: `./scripts/bench-fase1.sh` (cerca de 18 minutos). O script sai com erro se alguma das duas
decisões não for a esperada. Os experimentos desta rodada, com todas as execuções, estão em
[fase-1-experimentos.json](fase-1-experimentos.json).

## Execuções

p95 da janela estável, em ms, na ordem em que rodaram:

| # | A/A: lado | A/A: p95 | A/B: lado | A/B: p95 |
|---|---|---|---|---|
| 1 | A | 4,73 | A | 5,07 |
| 2 | B | 4,87 | B | 25,74 |
| 3 | B | 4,96 | B | 25,43 |
| 4 | A | 4,86 | A | 4,89 |
| 5 | A | 4,93 | A | 4,84 |
| 6 | B | 5,12 | B | 25,40 |
| 7 | B | 5,02 | B | 25,78 |
| 8 | A | 5,05 | A | 4,97 |
| 9 | A | 4,93 | A | 5,23 |
| 10 | B | 4,90 | B | 25,49 |

## Guardas e piso de ruído

| Experimento | Guarda | A | B | Diferença | Decisão da guarda |
|---|---|---|---|---|---|
| A/A | taxa de erro | 0% | 0% | 0 | sem diferença |
| A/A | CPU por requisição | 0,0873 | 0,0896 | +2,7% | inconclusivo |
| A/B | taxa de erro | 0% | 0% | 0 | sem diferença |
| A/B | CPU por requisição | 0,0870 | 0,1007 | +15,7% | piorou |

- **Piso de ruído medido pelo A/A: 5,3% no p95.** É a maior distância de zero do intervalo de 95% (+0,26 ms
  sobre 4,93 ms). Neste cenário e neste ambiente, um ganho menor que cerca de 10,5% não seria afirmado.
- O A/B usou esse piso: o experimento achou sozinho o A/A anterior do mesmo cenário e da mesma métrica.
- No A/A, a guarda de CPU por requisição saiu "inconclusivo" (o intervalo passa do limite prático de 10%
  dela). Isso não muda a decisão: só uma guarda que **piora** derruba o resultado.

## Limites desta prova

- **Só no modo Docker**, pelo mesmo motivo da Fase 0. No Kubernetes muda só quem é o alvo B (outro Deployment).
- **O efeito testado é grande** (cinco vezes o p95). A prova mostra que a regra acerta os dois casos do
  critério de pronto, não que ela detecta ganhos pequenos. Com o piso de 5,3% medido aqui, ganhos abaixo de
  ~10% dariam "inconclusivo" neste ambiente, que é o comportamento desejado.
- **Uma rodada de cada experimento.** Um A/A isolado dar "sem diferença" não mede a taxa de falso positivo
  da regra. A análise pede "na grande maioria das vezes"; medir isso exige repetir o A/A muitas vezes.
- **A janela estável teve cerca de 5 mil requisições por execução**, o bastante para o p95 e pouco para o p99.
- **Parar assim que a decisão sai** (a partir de 5 por lado) aumenta um pouco a chance de um resultado por
  acaso em relação a um número fixo de execuções. É o desenho da análise (seção 5.2); o limite prático e o
  piso de ruído são o que segura isso.
- **A tela de experimentos não foi aberta num navegador.** Passou na checagem de tipos e no build; a
  conferência visual fica com você.
