# Funcionalidades em detalhe

Referência completa do que o new-app-chaos faz hoje. Para rodar, veja o [README](../README.md).

## Alvos

Lista os containers Docker rodando (nome, imagem, portas, rede) e cria um cenário a partir de um deles.

## Cenários

Editor por formulário com alvo, URL base, endpoints (método, caminho, corpo, peso) e perfil de carga pronto (rampa, patamar ou pico) com prévia dos VUs. Salvos em SQLite.

## Execução

O backend sobe um container `grafana/k6` na mesma rede do alvo, lê as métricas já agregadas por segundo pelo dashboard embutido do k6 e transmite ao vivo (Server-Sent Events). Dá para abortar. Uma execução por vez.

## Gráficos ao vivo e histórico

Latência p50/p95/p99, RPS, VUs ativos, respostas por código HTTP, taxa de erro, CPU e memória do container alvo. Tudo agregado por segundo e salvo, então execuções antigas podem ser reabertas.

## Caos

Três modos por cenário (só carga, só caos, carga + caos). No editor, as falhas são blocos numa linha do tempo: arraste para mover, puxe as bordas para mudar a duração (setas do teclado também funcionam). Uma falha por vez neste marco. Falhas e parâmetros (nomes iguais aos do krkn):

- **Latência** (`latency`, `jitter` em ms) e **perda de pacotes** (`loss` em %): `tc netem` aplicado por um container auxiliar que entra na rede do alvo. Vale para o tráfego que sai do alvo, ou seja, as respostas da API.
- **CPU** (`cpu-load-percentage`, `cpu-workers`) e **memória** (`memory-vm-bytes`, ex. `256m`, `1g`, `80%`): um binário estático (`backend/cmd/stressor`) é copiado para dentro do alvo e executado lá, então a carga conta no limite do próprio container. Nada precisa ser instalado na imagem do alvo.
- **Kill/restart:** derruba o container no início do bloco e o sobe de novo no fim.
- Toda falha é revertida no fim do bloco, ao abortar e, se o backend reiniciar no meio, na subida seguinte. Se uma falha não puder ser aplicada ou revertida, a execução para e é marcada como falha.
- No modo só caos, uma **sonda** faz um GET por segundo no endpoint escolhido (ex. `/health`) e mostra latência, disponibilidade e erros, junto com CPU e memória do alvo.
- As faixas das falhas aparecem atrás de todos os gráficos, e os instantes reais de aplicação e reversão ficam salvos com a execução.

## Veredito (sem IA, reproduzível)

Cada cenário tem SLOs editáveis, com sugestão de p95 ≤ 500 ms e erro ≤ 1% nos modos com carga e disponibilidade da sonda ≥ 99% no modo só caos. Cada segundo da execução fica dentro ou fora deles, e a nota de resiliência (0 a 100) segue a ideia do Resiliency Score do krkn:

- cada falha vale 70% pelo tempo dentro dos SLOs enquanto ativa e 30% pela recuperação (cheia até 5 s, zero a partir de 60 s; recuperado = 3 s seguidos dentro dos SLOs);
- com falhas, a nota é 20% linha de base (tempo fora das falhas) + 80% média das falhas; sem falhas, é a porcentagem dos segundos dentro dos SLOs;
- 90 ou mais = resiliente, 70 a 89 = atenção, abaixo de 70 = frágil. Pesos em `backend/internal/verdict`.
- Segundos sem nenhuma resposta (alvo fora do ar) aparecem como lacuna nos gráficos e contam como fora dos SLOs. O timeout por requisição é configurável (padrão 10 s).

## Análise com IA, sob demanda

O botão "Analisar com IA" manda as evidências da execução (cenário, veredito, falhas com instantes reais, série por segundo com carga, sonda, CPU e memória) e salva um relatório com resumo, ponto de quebra, explicação da nota, efeito de cada falha, causas prováveis e correções.

- **Regressão:** marque uma execução como referência do cenário (☆ no veredito). As próximas análises do mesmo cenário trazem a comparação com ela. Nunca compara com outro cenário ou alvo.
- **Chat** em cada execução: a IA vê a execução, o relatório, a referência e o histórico do mesmo cenário. A conversa fica salva.
- **Código da aplicação:** no cenário, informe o repositório no GitHub (`dono/repo` ou URL), a branch e, se quiser, uma subpasta. Na análise e no chat a IA baixa essa branch (fica em cache por commit), navega no código só com leitura (listar pastas, buscar, abrir até 30 arquivos por análise) e as correções apontam arquivo e linha com link para o GitHub e o trecho proposto. O relatório registra o commit lido. Repositório privado precisa de `GITHUB_TOKEN` no `.env`. Sem repositório, nada muda.
- **Relatório imprimível:** o botão "Imprimir / salvar PDF" na execução gera um relatório em A4, sempre em tema claro, com números, veredito, falhas, gráficos e a análise da IA (sem o chat).
- **Configurações:** escolhe provedor e modelo (padrão Claude Opus 5.5; Sonnet 5.5 e Haiku 5.5 são mais baratos). A camada de provedor (`backend/internal/ai`) é trocável.

## Kubernetes

O app também roda dentro de um cluster KIND local (`make kind-up`). Lá o alvo é um **Deployment** (namespace + nome) em vez de um container:

- **Carga:** o k6 roda como um Job no namespace do app (`nac-k6-<execução>`); o backend lê o dashboard do k6 pelo IP do pod.
- **Falhas pelo Chaos Mesh:** latência e perda viram `NetworkChaos`, CPU e memória viram `StressChaos` e o kill vira `PodChaos pod-kill`. Cada falha tem a opção **um pod (sorteado) ou todos os pods**; o padrão é todos, e no kill é um. O recurso é criado no início do bloco, o app espera o Chaos Mesh confirmar a injeção e apaga no fim. O `duration` do recurso é o bloco + 30 s, uma rede de segurança se o backend cair.
- **Recursos:** CPU e memória somadas de todos os pods do Deployment (metrics-server, que atualiza a cada ~15 s) e um gráfico de **pods prontos x existentes**, que mostra a recuperação depois de um kill. A IA recebe as mesmas colunas.
- No kill do Kubernetes o pod é apagado no início do bloco e o Deployment recria sozinho; o bloco é o tempo em que a recuperação é observada (no Docker o container fica parado até o fim do bloco).
- **Falhas em dependências (fase B):** cada falha tem "Aplicar em": o alvo ou outro Deployment do cluster usado por ele (ex.: `pix/pix-postgres`). A carga continua no alvo e o veredito mede o efeito nele. Latência e perda podem ficar **só no caminho do alvo até a dependência** (NetworkChaos com `target` e `direction: both`, porque o alvo fala com a dependência pelo IP do Service e a regra só casa o IP dos pods na volta): o resto do tráfego segue normal.
- **Falhas só do Kubernetes (fase B):**
  - **Atraso de disco** e **erro de disco** (`IOChaos`): num volume do container (`volume-path`, ex.: `/var/lib/postgresql/data`; o Postgres do pix já sobe com esse volume), com `delay`, `errno` (5 = EIO) e `percent` das operações. Só aparece em operações que vão ao disco: um `SELECT` que o banco já tem em cache não sente, então use carga com escritas. O Postgres do pix guarda os dados num PVC, que sobrevive ao kill do pod.
  - **Erro de DNS** (`DNSChaos`): a resolução de nomes falha para os domínios em `patterns` (ex.: `pix-postgres.*`; vazio = todos). Só afeta conexões novas: um pool já aberto (ex.: Hikari com o banco) continua funcionando.
  - **Atraso HTTP** e **aborto HTTP** (`HTTPChaos`): nas requisições que chegam aos pods, filtradas por `port`, `path` e `method`. Não vale para HTTPS. Exigem o módulo `ebtables` no kernel dos nós, que o **WSL2 não tem**: no KIND sobre o WSL2 elas não são aplicadas.
  - As cores seguem o efeito: atrasos usam a cor da latência e erros a da perda; o rótulo do bloco diz qual é a falha e, se for numa dependência, onde (`@ pix-postgres`, ou `→ pix-postgres` só no caminho).
- **Falha que não confirma:** se o Chaos Mesh não confirma a injeção em 45 s (ou até o fim do bloco, se ele for mais curto), a falha fica marcada como "falhou" (com o erro do Chaos Mesh) e a execução segue com as próximas. O recurso é apagado em segundo plano, sem atrasar as próximas falhas, e, se o Chaos Mesh não o liberar, o app tira o finalizer dele para não deixar nada preso no cluster.
- **Logs e eventos na análise (fase C):** durante a execução o app coleta o log dos containers do alvo e das dependências usadas nas falhas (um stream do log de cada container, aberto assim que o pod aparece e mantido até ele terminar, então um pod apagado pelo kill fica com as linhas até o fim; a instância anterior de um container que reiniciou também entra) e, no fim, os eventos do Kubernetes desses namespaces (Killing, Unhealthy, BackOff...; do Chaos Mesh só Applied, Recovered e erros). Os eventos têm precisão de 1 s, então podem aparecer até ~1 s antes. No Docker entra o log do container alvo. Tudo fica salvo com a execução, aparece em "Logs e eventos" na tela dela (com a falha ativa em cada linha) e vai para a IA resumido: linhas iguais a menos de números juntadas, prioridade para erros, conexões, partidas e paradas, até ~40 mil caracteres.
- O modo Docker não mudou: `docker compose up` continua igual.

## Observabilidade opcional (Grafana)

Ligue com `make kind-up OBS=1` no KIND. Na Azure ela já vem ligada por padrão (opção do botão Ligar).
```
- O Grafana abre pelo próprio app, em http://localhost:3000/grafana/ (sem login e sem port-forward): o backend repassa para o Grafana do cluster. Há um botão **Grafana** na barra lateral, e cada execução no Kubernetes tem **Abrir no Grafana**, já no intervalo da execução e no serviço alvo.
- Instala a imagem [grafana/otel-lgtm](https://github.com/grafana/docker-otel-lgtm) no namespace `observability`: OpenTelemetry Collector, Prometheus, Tempo, Loki, Pyroscope e Grafana num pod só (~1-2 GB de RAM; dados em emptyDir). Na primeira vez o nó baixa ~1 GB.
- O pix-api passa a carregar o **OpenTelemetry Java agent** e o **Pyroscope Java agent**, sem reconstruir a imagem: um initContainer copia os jars (imagem `deploy/agents`, construída na sua máquina) e o `JAVA_TOOL_OPTIONS` os carrega. Saem métricas da JVM, HTTP e pool do banco a cada 5 s, traces e profiling de CPU e alocação.
- No fim de cada execução no Kubernetes, o backend espera ~10 s e lê da stack, para o alvo e as dependências usadas nas falhas:
  - **Prometheus:** heap, pausas de GC, threads, requisições recebidas (taxa, p95, 5xx), chamadas de saída, pool de conexões (em uso, esperando, tempo de espera), além da lista das outras métricas existentes.
  - **Tempo:** os traces com erro e os lentos (> 200 ms), com os spans que mais pesaram (SQL, exceção).
  - **Pyroscope:** as funções que mais consumiram CPU e as que mais alocaram memória.
- Tudo aparece em "Telemetria da aplicação" na tela da execução, com gráficos e as faixas das falhas, e vai para a IA.
- Rodar `./scripts/kind-up.sh` de novo sem `OBS=1` tira os agentes do pix-api e desliga a leitura (a stack continua instalada; apague com `kubectl delete ns observability`).
- Outros alvos: com `otel.enabled=true` no `deploy/targets/<alvo>.yaml`, o chart passa as variáveis `OTEL_*` (serviço, endpoint OTLP); `otel.java=true` adiciona os agentes. Os svc-* da Dorigao têm a própria instrumentação e ainda não foram ligados.
