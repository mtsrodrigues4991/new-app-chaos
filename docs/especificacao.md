# Especificação

A especificação viva do projeto (com o diagrama de arquitetura) está no documento:
https://claude.ai/artifact/DTb4tiMt1NnPTXaK39iZoo

## Decisões principais

| Tema | Decisão |
| --- | --- |
| Objetivo | Projeto pessoal/portfólio, com caminho aberto para open source ou SaaS |
| Alvo do MVP | APIs em containers Docker locais (depois VMs via SSH e Kubernetes) |
| Carga | k6, script gerado pelo backend a partir do cenário |
| Caos (MVP) | Latência, perda de pacotes, CPU, memória, kill/restart de container |
| Modos | Só carga, só caos, ou carga + caos na mesma linha do tempo |
| Interface | App web com linha do tempo visual para posicionar as falhas |
| IA | Resumo, ponto de quebra, regressões, causas e correções, veredito, chat; provedor trocável (Claude no MVP) |
| Stack | Backend Go, frontend React + TypeScript, SQLite, docker-compose |
