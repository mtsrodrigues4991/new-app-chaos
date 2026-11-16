# Fase 0, medição confiável: benchmark antes e depois

Data: 16/11/2026 · Antes: commit `4ace3b7` · Depois: Fase 0 de [analise-evolucao.md](../analise-evolucao.md)

## Resultado

**Com a carga por taxa de chegada, a mesma carga entrega o mesmo RPS com e sem latência injetada.**
Antes da fase, 200 ms de latência derrubavam a carga entregue em 95%.

| Carga | RPS sem latência | RPS com 200 ms | Diferença | Intervalo de 95% |
|---|---|---|---|---|
| Antes: VUs, backend `4ace3b7` | 1875,7 | 99,6 | −94,7% | −95,8% a −93,6% |
| Depois: taxa de chegada, backend novo | 250,0 | 250,0 | 0,0% | −0,2% a +0,2% |

Os três critérios de pronto da fase:

| Critério | Resultado | Evidência |
|---|---|---|
| A mesma carga entrega o mesmo RPS com e sem latência injetada | **Atendido** | Tabela acima: 5 execuções por condição, nenhuma requisição descartada. |
| A execução mostra o digest e o commit | **Atendido no Docker; parcial no Kubernetes** | As 20 execuções do backend novo gravaram `nacbench-demo-api@sha256:5a84a7e9…` e o commit `4ace3b70e791…`, lido do rótulo OCI da imagem. No KIND os pods reais já expõem o id da imagem que o código lê (conferido só com leitura); o commit passa a aparecer depois do próximo `make kind-up`, que é quem grava a anotação. Não rodei uma execução no KIND. |
| Um kill em `kube-system` pela API é recusado | **Atendido em teste** | `TestAPIRefusesChaosInSystemNamespace`: 400 ao salvar, ao editar e ao disparar um cenário já salvo. Não foi exercitado contra a AKS. |

## O que foi medido

A pergunta é a lacuna L2 da análise: a carga entregue ao alvo muda quando a latência dele muda?
Se muda, duas versões da aplicação recebem cargas diferentes e as latências não são comparáveis.

Três braços, cada um com e sem 200 ms de latência injetada no alvo:

| Braço | Backend | Carga | Para que serve |
|---|---|---|---|
| antes | `4ace3b7` | 20 VUs (modelo fechado) | O comportamento antes da fase. |
| controle | novo | os mesmos 20 VUs | Mostra se a medição nova (métricas por endpoint, janela estável) alterou o que é medido. |
| taxa | novo | 250 req/s por taxa de chegada, 150 VUs pré-alocados, 20 s de aquecimento | O comportamento depois da fase. |

O cenário é o mesmo nos três: perfil patamar de 60 s contra o `demo-api`, 90% `GET /items` e 10% `GET /compute`.
A latência é a falha `latency` do próprio app (`tc netem`), de 8 s a 58 s.

## Metodologia

Segue a seção 5.2 da análise.

| Regra da seção 5.2 | Como foi aplicada |
|---|---|
| A unidade é a execução, não o segundo | Cada execução vira um número (RPS médio da janela medida); a estatística é sobre as execuções. |
| Mínimo de 5 execuções por versão | 5 por condição, 30 no total. Todas concluíram e todas valeram. |
| Intercalar, nunca uma versão inteira antes da outra | Braços e condições intercalados; a ordem gira a cada repetição. |
| Descartar o aquecimento | Janela medida de 20 s a 54 s: depois da subida do perfil e da entrada da falha (aplicada em 8,2 s), antes da descida. |
| CPU do alvo estável | Alvo limitado a 2 CPUs (`--cpus 2`). |
| Gerador saturado invalida a execução | `dropped_iterations` foi zero nas 10 execuções por taxa de chegada. |
| Decisão pelo intervalo de 95% da diferença das medianas | Bootstrap com 20 mil reamostragens e semente fixa. |
| Uma execução por vez | Sim, com 5 s de pausa entre elas. |

Para os três braços serem medidos com o mesmo instrumento, o RPS vem das janelas de 1 s, que o backend
antigo também grava. O resumo novo da execução aparece em separado, na seção seguinte.

**Ambiente:** WSL2 (kernel 6.18), 12 CPUs, Docker 29.9, k6 0.54.0. Tudo em containers Docker numa rede própria,
sem tocar no KIND nem na Azure.

**Para repetir:** `./scripts/bench-fase0.sh` (cerca de 40 minutos). O script sobe o ambiente, roda o
[`scripts/bench-fase0/main.go`](../../scripts/bench-fase0/main.go) e apaga tudo no fim.
Os dados brutos desta rodada estão em [fase-0-execucoes.json](fase-0-execucoes.json).

## Números completos

Janela de 20 s a 54 s, mediana das 5 execuções:

| Braço | Latência injetada | RPS | RPS mínimo e máximo | p50 (ms) | p95 (ms) | Erro | CPU do alvo |
|---|---|---|---|---|---|---|---|
| antes | 0 | 1875,7 | 1855,6 a 1896,8 | 1,00 | 82,30 | 0% | 202% |
| antes | 200 ms | 99,6 | 99,4 a 99,9 | 200,40 | 205,10 | 0% | 7% |
| controle | 0 | 1868,8 | 1805,4 a 1912,0 | 1,10 | 80,40 | 0% | 202% |
| controle | 200 ms | 99,4 | 99,2 a 99,4 | 200,40 | 205,00 | 0% | 7% |
| taxa | 0 | 250,0 | 250,0 a 250,4 | 0,30 | 4,90 | 0% | 22% |
| taxa | 200 ms | 250,0 | 250,0 a 250,4 | 200,40 | 205,00 | 0% | 20% |

Diferença das medianas de RPS, com latência menos sem latência:

| Braço | Diferença | Intervalo de 95% | Em % da carga sem latência |
|---|---|---|---|
| antes | −1776,2 | −1797,3 a −1756,0 | −94,7% (−95,8% a −93,6%) |
| controle | −1769,4 | −1812,6 a −1706,0 | −94,7% (−97,0% a −91,3%) |
| taxa | 0,0 | −0,4 a +0,4 | 0,0% (−0,2% a +0,2%) |

**O controle não difere do antes.** Sem latência, a diferença das medianas de RPS entre o backend novo e o
antigo, com a mesma carga em VUs, foi de −6,9 (−0,4%), com intervalo de −78,2 a +39,1. O intervalo inclui
zero: as métricas novas no script do k6 não mudaram a carga gerada de forma mensurável.

### O que o resumo novo mostra

Braço da taxa de chegada, mediana das 5 execuções (janela estável: depois dos 20 s de aquecimento):

| | Sem latência | Com 200 ms |
|---|---|---|
| Requisições na execução inteira | 13.499 | 13.499 |
| Requisições na janela estável | 9.250 | 9.250 |
| p95 da janela estável | 4,92 ms | 205,06 ms |
| p99 da janela estável | 5,80 ms | 205,98 ms |
| p95 de `GET /items` | 0,53 ms | 200,57 ms |
| p95 de `GET /compute` | 6,19 ms | 206,20 ms |
| Requisições descartadas | 0 | 0 |

- O número de requisições é idêntico nas duas condições: a carga não dependeu da latência.
- Os endpoints aparecem separados. Sem latência, o p95 de `/compute` é mais de dez vezes o de `/items`; no
  número global (4,92 ms) isso não se vê.
- As 10 execuções em VUs do backend novo saíram com o aviso "carga em VUs (modelo fechado)", que as marca
  como impróprias para comparação. As 10 por taxa de chegada saíram sem aviso.

## Limites desta medição

- **Só no modo Docker.** O script do k6 e o leitor são os mesmos no Kubernetes, mas nenhuma execução
  rodou no KIND nem na AKS com o código novo. O teste real nesses ambientes fica com você.
- **A máquina não estava ociosa.** O cluster KIND estava no ar e, durante as primeiras execuções, rodei
  um `npm ci` e um build do frontend na mesma máquina. Com braços intercalados isso se espalha entre eles, e
  os intervalos acima já incluem esse ruído.
- **A janela estável teve 9.250 requisições**, abaixo das 10 mil que a seção 5.2 pede quando o p99 é a
  métrica principal. Aqui a métrica principal é o RPS, então não afeta a conclusão; para comparar p99 na
  Fase 1, use execuções mais longas ou taxa maior.
- **O RPS da janela estável do resumo (231,1) é menor que os 250 pedidos** porque essa janela vai até o fim
  da execução e inclui a descida do perfil patamar. É igual nas duas condições, que é o que importa para
  comparar; não é a taxa do patamar.
- **Os 200 ms são latência de rede, não de processamento.** A CPU do alvo não sobe com ela. O que o teste
  prova é a independência entre a taxa de chegada e o tempo de resposta.
- **O RBAC por namespace e a trava na AKS não foram exercitados num cluster.** Foram validados por
  `helm template`, `kubectl apply --dry-run=client` e testes de API. A primeira subida da Azure com eles é o teste real.
