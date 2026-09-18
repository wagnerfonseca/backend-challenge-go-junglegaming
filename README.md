# Distributed Wager Processing

Serviço Go de processamento distribuído de apostas: carteira com saldo exato, ledger auditável e append-only, idempotência persistente, HTTP e SQS compartilhando um único caso de uso financeiro, OIDC via Keycloak, PostgreSQL com locks por carteira e Uber Fx apenas na raiz de composição.

- Plano e critérios de aceite: [.specs/features/distributed-wager-processing/plan.md](.specs/features/distributed-wager-processing/plan.md)
- Checks com provas: [.specs/features/distributed-wager-processing/checks.md](.specs/features/distributed-wager-processing/checks.md)
- Decisões de arquitetura: [ARCHITECTURE.md](ARCHITECTURE.md)
- Enunciado original: [prompts/README.md](prompts/README.md)

## Pré-requisitos

Confira os pré-requisitos antes de iniciar a inicialização local.

- Go `1.27.1` (o `go.mod` fixa a versão)
- Docker com Compose (a pilha local usa PostgreSQL, Keycloak e LocalStack)
- `curl` e `python3` para os exemplos autenticados

## Inicialização com Docker Compose

```bash
docker compose up --build
```

O Compose sobe PostgreSQL, Keycloak com o realm importado, LocalStack com as filas provisionadas, aplica as migrações e inicia uma instância da aplicação. Quando todos os health checks passam:

```bash
curl http://localhost:8080/health/live     # {"status":"live"}
curl http://localhost:8080/health/ready    # {"status":"ready"}
```

Encerre com `Ctrl+C` ou `docker compose down -v`.

## Variáveis de ambiente

Todas as chaves e valores de exemplo estão em [.env.example](.env.example). Entre as variáveis de ambiente obrigatórias estão `DATABASE_URL`, `SQS_INGRESS_QUEUE_URL`, `SQS_EVENT_QUEUE_URL`, `OIDC_ISSUER_URL` e `OIDC_AUDIENCE`; `SQS_PROVIDER_SENDER_IDS` registra as identidades de provedor aceitas na fila. Falhas de configuração ou de dependência impedem o startup com saída não zero.

## IdP, identidades e filas

O realm `wager` é importado de [deploy/keycloak/realm.json](deploy/keycloak/realm.json). Tokens vêm de `client_credentials` no issuer `http://keycloak:8080/realms/wager` (do host, o endpoint fica em `http://localhost:8180/realms/wager`).

| Cliente | Escopos | `provider_id` | Uso |
| --- | --- | --- | --- |
| `provider-a` | `wagering:write`, `wagering:read` | `provider-a` | Provedor A |
| `provider-b` | `wagering:write`, `wagering:read` | `provider-b` | Provedor B |
| `provider-expiring` | `wagering:write`, `wagering:read` | `provider-a` | Token de 1 s para provar expiração |
| `internal-service` | `wallets:write`, `wallets:read`, `reconciliation:execute`, `metrics:read`, `wagering:read` | ausente | Cliente interno |

Credenciais locais dos clientes: `local-provider-a`, `local-provider-b`, `local-provider-expiring`, `local-internal-service`.

Filas criadas por `cmd/provision` e pelo serviço `provision` do Compose:

| Fila | Papel |
| --- | --- |
| `wager-transactions.fifo` | Entrada de apostas; `MessageGroupId=walletId`, `MessageDeduplicationId=messageId` |
| `wager-transactions-dlq.fifo` | Dead-letter com redrive após 5 recebimentos e retenção de 14 dias |
| `wager-events.fifo` | Eventos de saída; `MessageGroupId=walletId`, `MessageDeduplicationId=eventId` |

Cada provedor publica com uma identidade IAM própria; o consumidor mapeia o `SenderId` para o provedor configurado e exige igualdade com `data.providerId`. No Compose, `SQS_PROVIDER_SENDER_IDS` registra `sender-a=provider-a,sender-b=provider-b`.

## Migrações

```bash
go run ./cmd/migrate -direction up      # aplica
go run ./cmd/migrate -direction down    # reverte
go run ./cmd/migrate --validate         # aplica, reverte e reaplica
```

As migrações ficam em `migrations/` com pares `*.up.sql`/`*.down.sql`. Cada migração é aplicada uma única vez e uma falha retorna saída não zero; uma versão `dirty` precisa ser corrigida antes de nova tentativa.

## Chamadas autenticadas

Obtenha um token de provedor e um token interno:

```bash
PROVIDER_TOKEN=$(curl -s -X POST http://localhost:8180/realms/wager/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=provider-a -d client_secret=local-provider-a \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')

INTERNAL_TOKEN=$(curl -s -X POST http://localhost:8180/realms/wager/protocol/openid-connect/token \
  -d grant_type=client_credentials -d client_id=internal-service -d client_secret=local-internal-service \
  | python3 -c 'import json,sys; print(json.load(sys.stdin)["access_token"])')
```

Abra uma carteira (cliente interno), consulte-a, envie uma aposta (provedor) e reconcilie:

```bash
PLAYER_ID=$(python3 -c 'import uuid; print(uuid.uuid4())')
curl -s -X POST http://localhost:8080/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" -H 'Content-Type: application/json' \
  -d "{\"playerId\":\"$PLAYER_ID\",\"initialBalance\":{\"amount\":\"1000.00\",\"currency\":\"BRL\"}}"

curl -s http://localhost:8080/wallets/$WALLET_ID -H "Authorization: Bearer $INTERNAL_TOKEN"
curl -s "http://localhost:8080/wallets/$WALLET_ID/ledger?limit=50" -H "Authorization: Bearer $INTERNAL_TOKEN"

curl -s -X POST http://localhost:8080/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_TOKEN" -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: exemplo-1' \
  -d "{\"externalTransactionId\":\"ext-1\",\"playerId\":\"$PLAYER_ID\",\"walletId\":\"$WALLET_ID\",\"roundId\":\"round-1\",\"gameId\":\"game-1\",\"kind\":\"BET\",\"money\":{\"amount\":\"25.00\",\"currency\":\"BRL\"}}"

curl -s -X POST http://localhost:8080/wallets/$WALLET_ID/reconciliation -H "Authorization: Bearer $INTERNAL_TOKEN"
```

O cliente interno também consulta transações por ID interno e o provedor consulta as suas por `/providers/{providerId}/wagering/transactions/{externalTransactionId}`. `GET /metrics` exige o escopo `metrics:read` e retorna texto Prometheus. As rotas `GET /health/live` e `GET /health/ready` não exigem credencial.

## Testes e verificação

```bash
go test ./...                                        # unitários
go test -tags integration ./internal/integration/    # PostgreSQL, Keycloak e LocalStack reais
go test -race ./...                                  # unitários com detector de corrida
go test -race -tags integration ./internal/integration/
go vet ./...
gofmt -l .
```

A suíte de integração sobe containers reais e usa subprocessos para as provas distribuídas. Os cenários mais pesados têm nomes próprios: `TestThreeProcessIntegration`, `TestProcessRestartIntegration`, `TestConsumerInterruption`, `TestOutboxPublisherRace`, `TestReferenceTimingIntegration`, `TestHTTPAndSQSIntegration`, `TestBalanceVerification` e `TestFxLifecycle`.

## Três instâncias

O cenário de três processos independentes, com memória e conexões separadas, roda na suíte:

```bash
go test -tags integration ./internal/integration/ -run TestThreeProcessIntegration -v
```

Cada subprocesso usa uma porta própria, o mesmo PostgreSQL e as mesmas filas; o banco permanece a autoridade financeira. O `TestProcessRestartIntegration` derruba e reinicia as três instâncias preservando idempotência, referências pendentes, outbox, saldos e ledger.

## DLQ e redrive

O redrive move para `wager-transactions-dlq.fifo` após 5 recebimentos. Para inspecionar e redirecionar mensagens manualmente:

```bash
docker compose exec localstack awslocal sqs get-queue-attributes \
  --queue-url http://localhost:4566/000000000000/wager-transactions-dlq.fifo \
  --attribute-names ApproximateNumberOfMessages

docker compose exec localstack awslocal sqs start-message-move-task \
  --source-arn arn:aws:sqs:us-east-1:000000000000:wager-transactions-dlq.fifo
```

O redrive devolve a mensagem à inbox, que deduplica por `(consumerName,messageId)` e digest. As provas são `TestDLQAfterFiveReceives` e `TestPermanentMessageDLQ`.

## Simulação de falhas

Failpoints só são aceitos com `APP_ENV=integration`; configuração de produção com `FAILPOINTS` é rejeitada no startup. Os pontos disponíveis são `http.after_commit`, `sqs.after_commit`, `sqs.after_delete`, `outbox.after_publish` e `reference.after_resolve`.

```bash
APP_ENV=integration FAILPOINTS=sqs.after_commit go run ./cmd/service
go test -tags integration ./internal/integration/ -run TestConsumerInterruption -v
```

As provas de interrupção reiniciam processos nos dois lados do commit e confirmam que nenhum movimento financeiro se duplica.
