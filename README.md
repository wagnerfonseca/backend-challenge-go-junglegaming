# Distributed Wager Processing

Desafio backend para implementar um serviço Go de processamento distribuído de apostas, com carteira, ledger auditável, idempotência persistente, HTTP, SQS, PostgreSQL, OIDC e Uber Fx.

## Estado atual

Este repositório contém o enunciado e a especificação de implementação. A aplicação ainda não foi implementada.

- Requisitos funcionais e técnicos: [prompts/README.md](prompts/README.md)
- Plano de execução e critérios de aceite: [.specs/features/distributed-wager-processing/plan.md](.specs/features/distributed-wager-processing/plan.md)
- Módulo Go: `github.com/wagnerfonseca/backend-challenge-go-junglegaming`

## Próximos passos

1. Implementar o domínio financeiro sem dependências de infraestrutura.
2. Adicionar persistência PostgreSQL, HTTP, SQS, OIDC, outbox e workers.
3. Documentar a execução local e validar os critérios com testes unitários e de integração.