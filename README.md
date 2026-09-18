# Distributed Wager Processing

Desafio backend para implementar um serviço Go de processamento distribuído de apostas, com carteira, ledger auditável, idempotência persistente, HTTP, SQS, PostgreSQL, OIDC e Uber Fx.

## Estado atual

O núcleo de domínio está implementado em `internal/domain` (dinheiro, entidades financeiras e contrato de eventos) com os testes unitários verdes. Persistência, HTTP, SQS, OIDC e workers ainda não existem.

- Requisitos funcionais e técnicos: [prompts/README.md](prompts/README.md)
- Plano de execução e critérios de aceite: [.specs/features/distributed-wager-processing/plan.md](.specs/features/distributed-wager-processing/plan.md)
- Checks com provas: [.specs/features/distributed-wager-processing/checks.md](.specs/features/distributed-wager-processing/checks.md)
- Módulo Go: `github.com/wagnerfonseca/backend-challenge-go-junglegaming`

## Estrutura

- `cmd/` — pontos de entrada (serviço e migrações)
- `internal/domain/` — modelo financeiro sem infraestrutura
- `internal/application/` — casos de uso e portas
- `internal/adapters/` — HTTP, PostgreSQL, SQS, OIDC e métricas
- `internal/integration/` — suíte de integração com infraestrutura real
- `migrations/` — migrações up/down

## Próximos passos

1. Implementar a camada de aplicação e a persistência PostgreSQL.
2. Adicionar HTTP, SQS, OIDC, outbox e workers.
3. Documentar a execução local e validar os critérios com testes unitários e de integração.