# Arquitetura

Documento orientado a decisões: cada seção registra o que foi decidido, o mecanismo que sustenta a decisão e a prova que a mantém verdadeira. As provas são testes nomeados em `internal/integration` ou testes de domínio em `internal/domain`.

## Regra de dependência

O núcleo é hexagonal: `domain <- application <- adapters`. O domínio não importa Fx, HTTP, SQS, PostgreSQL, OIDC, Prometheus nem logging; HTTP e SQS invocam o mesmo caso de uso financeiro. A prova é `go build ./internal/domain/...` e a suíte de integração que cruza as duas entradas (`TestHTTPAndSQSIntegration`).

## Money

Valores monetários usam `int64` em unidades mínimas com moeda, nunca `float`. A entrada externa é canônica (`^(0|[1-9][0-9]*)\.[0-9]{2}$`), o máximo é `92233720368547758.07` e toda operação verifica overflow e incompatibilidade de moeda. A persistência é `BIGINT` e o wire é string decimal. Provas: `TestMoneyParseAndSerialize`, `TestMoneyOverflow`, `TestMoneyCurrencyMismatch`, `TestReconciliationCalculation`.

## Fronteira SQL

Todo acesso passa por `pgx/v5` com SQL explícito e parametrizado; não há ORM nem migração automática no boot. Migrações versionadas `up`/`down` ficam em `migrations/` e são aplicadas por `cmd/migrate`, com validação de ida e volta. O ledger é append-only: a role da aplicação recebe `INSERT, SELECT` e nenhum `UPDATE`/`DELETE`, e a aritmética `balanceAfter = balanceBefore ± amount` é uma constraint. Provas: `TestMigrationReversal`, `TestLedgerWriteDeniedForAppRole`, `TestCommitAtomicity`.

## Idempotência

HTTP e SQS compartilham uma projeção de negócio canônica (`sha256-jcs-v1`) sobre provedor, transação externa, jogador, carteira, rodada, jogo, tipo, valor e referência. O banco garante unicidade por `(providerId,idempotencyKey)` e `(providerId,externalTransactionId)`; replay equivalente devolve o snapshot original sem novo movimento, e projeção diferente é conflito. A decisão sobrevive a reinícios porque mora no PostgreSQL, não na memória. Provas: `TestIdempotencyDigest`, `TestIdempotentReplay`, `TestIdempotencyConflict`, `TestRestartIdempotencyPreserved`, `TestParallelFiftyBets`.

## Locks e concorrência

A operação que pode mudar uma carteira trava a linha com `SELECT ... FOR UPDATE` antes de ler o saldo da decisão; carteiras distintas não se bloqueiam. Não existe lock global de processo ou de banco: a coordenação é por linha. Provas: `TestCrossWalletConcurrency`, `TestDistinctWalletsConcurrent`, `TestConcurrentBetsIntegration`, `TestLostUpdatePrevention`, `TestThreeProcessIntegration`.

## Máquina de estados

`WagerTransaction` aceita somente `PENDING -> PENDING_REFERENCE|PROCESSED|REJECTED|FAILED` e `PENDING_REFERENCE -> PROCESSED|REJECTED|FAILED`; estados terminais são imutáveis e operações sem dependência nunca confirmam `PENDING` intermediário. Provas: `TestStateMachineTransitions`, `TestTerminalStateImmutable`, `TestImmediateTerminalTransition`.

## Código de falha

Rejeições financeiras duráveis persistem um `failureCode` estável: `INSUFFICIENT_FUNDS`, `REVERSAL_INSUFFICIENT_FUNDS`, `REFERENCE_NOT_FOUND`, `REFERENCE_NOT_PROCESSED`, `REFERENCE_MISMATCH`, `REVERSAL_AMOUNT_MISMATCH`, `REFERENCE_KIND_NOT_ALLOWED`, `INVALID_WIN_REFERENCE`, `ALREADY_REVERSED` e `PERMANENT_INFRASTRUCTURE_FAILURE`. A resposta HTTP de uma rejeição aceita continua sendo resultado de transação (`200` com `status:"REJECTED"`), não erro de transporte. Provas: `TestRejectedPersistsFailureCode`, `TestTransactionStatusExposure`, `TestErrorEnvelope`.

## Classificador transitório/permanente

Indisponibilidade de PostgreSQL ou SQS é transitória: HTTP responde `503` antes do commit e o SQS mantém a mensagem para reentrega com backoff de visibilidade `5s/10s/20s/40s` por recebimento. `FAILED` com `PERMANENT_INFRASTRUCTURE_FAILURE` é reservado a trabalho assíncrono já aceito cujo registro persistido viola um invariante. Mensagem estruturalmente inválida é permanente e segue o redrive até a DLQ. Provas: `TestDatabaseUnavailableHandling`, `TestSQSVisibilityBackoff`, `TestPermanentFailureNoMovement`, `TestInvalidMessagePermanent`.

## Referência pendente

Quando uma referência obrigatória (ou a referência opcional de `WIN`) ainda não existe, a transação é aceita como `PENDING_REFERENCE` com prazo de 24 horas, backoff exponencial de 1 segundo a 15 minutos e jitter determinístico de até 10%; o agendamento é durável e sobrevive a reinícios. Referência pendente mantém a dependente em espera; referência rejeitada ou falha encerra a dependente com `REFERENCE_NOT_PROCESSED`; o prazo expira em `REFERENCE_NOT_FOUND`. Provas: `TestPendingReferenceCommit`, `TestReferenceRetryBackoff`, `TestReferenceExpiry`, `TestReferenceTimingIntegration`, `TestProcessRestartIntegration`.

## Reversão

O primeiro `REFUND` ou `ROLLBACK` direto processado de uma `BET` consome permanentemente o direito de compensação; uma segunda tentativa termina em `ALREADY_REVERSED`. O rollback de um refund debita o valor do refund sem reabrir a bet. A exclusividade é garantida por `UNIQUE(referenceTransactionId)` e `UNIQUE(reversalTransactionId)` no banco, independentemente do FIFO. Provas: `TestCompensationRightConsumed`, `TestAlreadyReversed`, `TestRollbackRefund`, `TestWINDoubleRollback`.

## Inbox

Cada entrega SQS é registrada por `(consumerName,messageId)` com digest do payload; a conclusão da inbox confirma na mesma transação SQL do resultado financeiro. Reentrega com o mesmo digest remove a mensagem sem novo movimento; digest diferente é conflito permanente `INBOX_PAYLOAD_CONFLICT`. Trabalho sem commit durável nunca é removido da fila. Provas: `TestInboxTransactionCommit`, `TestInboxDuplicateDelete`, `TestInboxPayloadConflict`, `TestInboxNoDeleteOnFailure`, `TestConsumerInterruption`.

## SQS, visibilidade e redrive

O consumo usa long poll de 20 s, lotes de até 10, visibility de 60 s renovada a cada 20 s e redrive após 5 recebimentos para `wager-transactions-dlq.fifo` (retenção de 14 dias). O shutdown aguarda até 30 s pelo trabalho em andamento e libera a visibilidade do que não terminar. Provas: `TestSQSPollingParameters`, `TestSQSShutdownGrace`, `TestDLQAfterFiveReceives`, `TestPermanentMessageDLQ`.

## Outbox

Eventos aplicáveis já existem como snapshot na outbox no commit financeiro; o request path e o consumidor nunca publicam direto. O publicador reivindica lotes de até 50 com lease recuperável de 30 s, republica com o mesmo `eventId` e tenta indefinidamente com backoff de 1 s a 5 min. Processo interrompido depois do commit tem o evento recuperado por outra instância. Provas: `TestOutboxAtCommit`, `TestOutboxLeaseAndBatch`, `TestOutboxStableIdRepublish`, `TestOutboxRecovery`, `TestOutboxPublisherRace`.

## Contrato de consumo de eventos

O envelope tem `eventId`, `eventType`, `aggregateId`, `correlationId`, `causationId` opcional, `occurredAt`, `version:1` e `data` tipado; timestamps são UTC RFC 3339 com milissegundos e dinheiro é string decimal. A publicação usa `MessageGroupId=walletId` e `MessageDeduplicationId=eventId`. Provas: `TestEventEnvelopeStructure`, `TestEventSerialization`, `TestEventDataSchemas`, `TestOutboxMetadataStability`.

## Autenticação

Keycloak é o IdP local com realm importado e `client_credentials`. O adaptador OIDC valida assinatura via JWKS descoberto no startup, `iss`, `aud`, expiração e escopo; discovery inválido ou indisponível impede o startup. O token de provedor carrega `provider_id`; o token interno não. Provas: `TestOIDCTokenValidation`, `TestAuthIntegration`, `TestStartupFailure`.

## Autorização

O mapa de escopos exige `wallets:write`, `wallets:read`, `reconciliation:execute`, `metrics:read`, `wagering:write` e `wagering:read` por rota. Rotas internas rejeitam credencial de provedor com `403`; rotas de provedor exigem `provider_id` e o valor autenticado é a autoridade, nunca o corpo ou o path. Provedor só lê transações próprias; a de outro provedor é `404` sem campos, e `OPENING` interno é invisível a provedores. Provas: `TestScopeEnforcement`, `TestProviderAuthority`, `TestProviderIdMismatch`, `TestCrossProviderQuery`, `TestProviderForbiddenRoutes`, `TestInternalAccessNoProvider`.

## Fx e encerramento

Fx existe apenas na raiz de composição e no lifecycle: configuração, conexões, repositórios, casos de uso, handlers e workers são montados com `fx.Module`, `fx.Provide` e `fx.Invoke`, e trabalho longo é anexado ao `fx.Lifecycle`. `SIGTERM` para de aceitar HTTP e de pollar SQS antes de fechar PostgreSQL e SQS; o encerramento respeita o orçamento de 30 s e nenhum recurso fecha antes dos workers. Provas: `TestSIGTERMOrdering`, `TestShutdownOrder`, `TestFxLifecycle`.

## Limitações e trabalho pendente

- Sem tracing distribuído, dashboards ou rate limit distribuído de negócio; os limites são por instância e protegem recursos.
- A reconciliação apenas reporta divergências: não corrige saldos automaticamente.
- Reversões são sempre integrais; não há reversão parcial nem ledger de partidas dobradas.
- Apenas `BRL` é aceito nas entradas externas; `USD` existe só para provar incompatibilidade no domínio.
- Não há retenção, arquivamento ou expurgo automático de registros financeiros; a DLQ retém por 14 dias.
- Não há interface web, cadastro de usuários nem emissão própria de tokens.
- Deploy, push remoto e alteração de dados de produção estão fora desta entrega.
- Cada limitação acima é consciente; trabalho pendente de evolução contratual deve usar nova rota ou media type, nunca alterar a v1 em vigor.
