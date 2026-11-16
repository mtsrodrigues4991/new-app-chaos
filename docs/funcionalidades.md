# Funcionalidades em detalhe

Referência completa do que o new-app-chaos faz hoje. Para rodar, veja o [README](../README.md).

## Alvos

Lista os containers Docker rodando (nome, imagem, portas, rede) e cria um cenário a partir de um deles.

## Cenários

Editor por formulário com alvo, URL base, endpoints (método, caminho, corpo, peso) e perfil de carga pronto (rampa, patamar ou pico) com prévia. Salvos em SQLite.

O perfil tem dois modelos de carga:

- **Usuários virtuais** (padrão): cada VU espera a resposta antes do próximo pedido. Se a API fica lenta, a carga cai junto. Bom para achar o ponto de quebra; ruim para comparar duas versões, porque a mais rápida recebe mais carga.
- **Taxa de chegada:** requisições por segundo fixas (executor `ramping-arrival-rate` do k6), responda a API rápido ou devagar. É o modelo para comparar antes e depois. Pede também os **VUs pré-alocados**, o teto de requisições simultâneas: taxa × tempo de resposta esperado em segundos, com folga. Se faltar VU, o k6 descarta requisições e a execução avisa que não serve para comparação.

O **aquecimento** (segundos, opcional) tira das medições da janela estável as requisições iniciadas no começo do teste, quando JIT, pools e caches ainda estão enchendo.

## Execução

O backend sobe um container `grafana/k6` na mesma rede do alvo, lê as métricas já agregadas por segundo pelo dashboard embutido do k6 e transmite ao vivo (Server-Sent Events). Dá para abortar. Uma execução por vez.

## Gráficos ao vivo e histórico

Latência p50/p95/p99, RPS, VUs ativos, respostas por código HTTP, taxa de erro, CPU e memória do container alvo. Tudo agregado por segundo e salvo, então execuções antigas podem ser reabertas.

## Medição da execução

Quando a execução termina, o cartão **Medição** (aba Resumo) mostra o que serve para comparar duas execuções:

- **Versão testada:** a imagem e o identificador do conteúdo (digest do registry ou id da imagem local) do alvo e das dependências usadas nas falhas, lidos no início da execução. O commit aparece quando é declarado: pelo rótulo OCI `org.opencontainers.image.revision` da imagem (Docker) ou pela anotação de mesmo nome no pod (Kubernetes; o `kind-up.sh` preenche os dois quando a pasta do alvo não tem mudança local). Sem declaração, a tela diz "não declarado" em vez de supor. Pods com versões diferentes durante o teste ficam marcados.
- **Números da execução inteira, da janela estável e por endpoint:** requisições, vazão, erro, p50, p95, p99 e máximo, calculados pelo k6 sobre todas as requisições do trecho (e não sobre os percentis de cada segundo). A janela estável e os endpoints contam só as requisições iniciadas depois do aquecimento. Até 20 endpoints têm números próprios; os demais entram só nos totais.
- **Avisos:** a execução diz quando não serve para comparação (carga em VUs, requisições descartadas por falta de VU, nenhuma requisição na janela estável).

A IA recebe a versão e esses números junto das outras evidências.

## Experimentos A/B (antes e depois, com estatística)

Um experimento roda o mesmo cenário contra duas versões e decide, sem IA, se a versão B **melhorou**, **piorou**, **não difere** ou se o resultado é **inconclusivo**.

- **Quem implanta é você.** A é o alvo do cenário; B é outro container ou Deployment com a versão candidata. Sem B, é um **teste A/A** (a mesma versão dos dois lados), que mede o ruído do cenário naquele ambiente. Faça um antes do primeiro A/B.
- **Execuções repetidas e intercaladas:** A B, B A, A B... O experimento decide a partir de 5 execuções válidas por versão e para assim que a decisão deixa de ser "inconclusivo", ou no teto (10 por versão, ajustável). Enquanto ele roda, nenhuma outra execução entra; se já houver uma rodando, ele espera a vez.
- **Métrica principal escolhida antes:** p50, p95, p99 ou média de latência, taxa de erro (da janela estável, de todos os endpoints ou de um só) ou a nota de resiliência (cenários com falhas).
- **Regra de decisão** (pacote `internal/stats`): intervalo de 95% da diferença das medianas, por bootstrap com semente fixa, contra um limite prático (padrão: 5% da mediana de A).
  - *Sem diferença:* o intervalo inteiro cabe dentro do limite prático.
  - *Melhorou:* o intervalo exclui zero para o lado bom e o ganho passa do limite prático.
  - *Piorou:* o intervalo exclui zero para o lado ruim, ou uma métrica de guarda piorou.
  - *Inconclusivo:* todo o resto. É um resultado legítimo: os dados não bastam.
- **Guardas:** taxa de erro, nota de resiliência e CPU do alvo por requisição. Baixar o p95 dobrando os erros não é ganho.
- **Piso de ruído:** o teste A/A registra o quanto a métrica varia sem nenhuma mudança. Num A/B, um ganho menor que duas vezes esse piso vira "inconclusivo", e a tela avisa quando o cenário nunca teve um A/A.
- **O que invalida uma execução:** não concluir, falha que não foi aplicada e revertida, requisições descartadas pelo gerador, nenhuma requisição na janela estável ou pods com versões diferentes. Ela é registrada com o motivo e não entra na conta. Se a imagem de um lado mudar no meio do experimento, o resultado é "inconclusivo".
- **Exige carga por taxa de chegada** nas métricas de latência e erro: com carga em VUs a versão mais rápida receberia mais carga.
- A execução de um experimento mostra de qual experimento e de que lado ela é. As do lado B não entram no histórico que o chat da IA usa (são de outro alvo).

## Correções automáticas: da sugestão ao PR em rascunho

Com o código da aplicação configurado no cenário, cada correção do relatório da IA vem também como **patch** (diff unificado), com a métrica que deve melhorar e o quanto.

- **Patch conferido:** o backend roda `git apply --check` contra o commit analisado e mostra "aplica" ou "não aplica" ao lado da sugestão.
- **Tentar esta correção:** registra uma tentativa de transformar a sugestão num pull request. **Nada roda antes de uma pessoa aprovar** (com o login ligado, só o administrador aprova ou recusa; fica registrado quem pediu e quem decidiu).
- **Limites de escopo, conferidos sobre o diff e não sobre o prompt:** até 5 arquivos e 200 linhas; nada em pipelines de CI, arquivos de segredo, código de autenticação ou migrações de banco; em arquivo de teste só se acrescenta (apagar ou alterar um teste existente barra a tentativa); mudança em arquivo de dependências não barra, mas é destacada para quem aprova e no PR. Valem para o patch sugerido e para tudo que o agente escrever.
- **Sandbox** (só no Kubernetes, namespace próprio): cada rodada é um pod sem conta de serviço, sem root e com Pod Security "restricted", em quatro passos:
  1. *checkout:* baixa o commit testado e tenta aplicar o patch como veio;
  2. *agente:* só roda se o patch não aplicou ou se o build da rodada anterior falhou. É o Claude Code em modo restrito, só com ferramentas de arquivo (não executa comandos nem acessa a rede), ignorando o `CLAUDE.md` e os hooks do repositório, com teto de custo. É o único passo que vê a chave da Anthropic;
  3. *build e testes:* a imagem e o comando configurados no cenário (ex.: `maven:3.9-eclipse-temurin-17` e `mvn -B -q verify`). Não vê credencial nenhuma;
  4. *relatório:* escreve o diff e o resultado no log, que é por onde o backend lê.
  A rede do namespace só sai para o DNS e para a internet (as faixas privadas ficam bloqueadas, o que isola os alvos, o banco do app e a observabilidade). Isso depende de o cluster aplicar NetworkPolicy.
- **Até 3 rodadas e US$ 3 por tentativa.** Se o build ou os testes falham, o agente recebe o fim do log e tenta de novo. Sem build verde, nenhum PR é aberto.
- **Publicação sem modelo:** com o build verde, um passo determinístico do backend cria o branch `nac/fix-<id>` com um commit sobre o commit testado e abre o PR **em rascunho**, com a evidência (execução, commit, comando de teste, hipótese de ganho, quem aprovou). Usa o `PR_GITHUB_TOKEN`, um token de escrita separado do de leitura, que o agente nunca vê. O app não faz merge, não aprova PR e não reescreve branch.
- **Auditoria:** cada tentativa guarda o patch sugerido, as instruções dadas ao agente, o diff e o log de cada rodada, o custo e o resultado. Tentativas interrompidas por reinício do backend não são retomadas.
- **Configuração:** no cenário, em "Código da aplicação", a imagem de build e o comando de teste. No ambiente, `PR_GITHUB_TOKEN` no `.env` (token fine-grained com Contents e Pull requests em leitura e escrita, só nos repositórios alvo). O `make kind-up` já liga o sandbox; na Azure ele fica desligado (o cluster de hoje não aplica NetworkPolicy).

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
- **Código da aplicação:** no cenário, informe o repositório no GitHub (`dono/repo` ou URL), a branch e, se quiser, uma subpasta. Na análise e no chat a IA baixa o commit que estava implantado durante o teste, quando a imagem do alvo o declara (ver "Medição da execução"); sem isso, baixa o topo da branch e o relatório avisa que pode não ser o código que rodou. A cópia fica em cache (os três commits usados por último de cada repositório). A IA, navega no código só com leitura (listar pastas, buscar, abrir até 30 arquivos por análise) e as correções apontam arquivo e linha com link para o GitHub e o trecho proposto. O relatório registra o commit lido. Repositório privado precisa de `GITHUB_TOKEN` no `.env`. Sem repositório, nada muda.
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
- **Onde as falhas podem entrar:** o alvo e as dependências nunca podem estar num namespace de sistema (`kube-system` e outros `kube-*`, `chaos-mesh`, `observability`, `cert-manager`, `traefik`, `ingress-nginx`, `gatekeeper-system`, `openshift-*`, `local-path-storage`) nem no do próprio app: a API recusa o cenário ao salvar e ao disparar. Com `ALLOWED_TARGET_NAMESPACES` (valor `guard.allowedTargetNamespaces` do chart), só os namespaces listados são aceitos. O valor `rbac.targetNamespaces` do chart vai além: o backend só recebe permissão de criar falhas nesses namespaces (Role por namespace no lugar do ClusterRole), e o próprio Kubernetes recusa o resto.
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
