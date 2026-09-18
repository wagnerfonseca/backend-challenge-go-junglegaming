// Package app is the Fx composition root. Fx lives only here: configuration,
// connections, repositories, use cases, handlers and workers are composed with
// fx.Module, fx.Provide and fx.Invoke, and long-running work is attached to
// fx.Lifecycle.
package app

import (
	"context"
	"log/slog"
	"os"
	"sync"
	"time"

	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/failpoint"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/health"
	httpadapter "github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/auth"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/middleware"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/http/server"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/logging"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/metrics"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/outbox"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/postgres"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/sqs/consumer"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/worker"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/application"
)

// Recorder observes lifecycle transitions. The default is inert; tests supply
// an ordered recorder to prove shutdown ordering.
type Recorder interface {
	Record(event string)
}

type noopRecorder struct{}

func (noopRecorder) Record(string) {}

// New builds the application graph. Extra fx options let tests replace the
// authenticator or the lifecycle recorder.
func New(cfg config.Config, extras ...fx.Option) (*fx.App, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	logger := logging.New(cfg.ServiceName, cfg.InstanceID, cfg.LogLevel, os.Stdout)
	registry := metrics.NewRegistry()
	bundle := metrics.New(registry)
	failpoints := failpoint.New(cfg.Failpoints)

	options := []fx.Option{
		fx.Supply(cfg, logger, registry, bundle, failpoints),
		fx.Provide(func() Recorder { return noopRecorder{} }),
		fx.Provide(newClock, newAuthenticator, newPool, newQueueClient, newStore, newService, newReadiness,
			newHTTPHandler, newConsumer, newEventSender, newOutboxPublisher, newReferenceWorker),
		fx.Invoke(registerHTTPServer, registerConsumer, registerOutboxPublisher, registerReferenceWorker),
		fx.StartTimeout(15 * time.Second),
		fx.StopTimeout(30 * time.Second),
	}
	options = append(options, extras...)
	return fx.New(options...), nil
}

func newClock() application.Clock { return application.SystemClock{} }

// newAuthenticator builds the OIDC boundary from the configured issuer. An
// unavailable or inconsistent discovery document fails startup before the
// service can become ready.
func newAuthenticator(lc fx.Lifecycle, cfg config.Config) (middleware.Authenticator, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	authenticator, err := auth.NewOIDC(ctx, cfg.OIDC.IssuerURL, cfg.OIDC.Audience)
	if err != nil {
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error {
		authenticator.Close()
		return nil
	}})
	return authenticator, nil
}

func newPool(lc fx.Lifecycle, cfg config.Config, recorder Recorder, logger *slog.Logger) (*pgxpool.Pool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, cfg.Database.URL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	lc.Append(fx.Hook{OnStop: func(context.Context) error {
		pool.Close()
		logger.Info("postgres pool closed")
		recorder.Record("postgres.closed")
		return nil
	}})
	return pool, nil
}

func newQueueClient(lc fx.Lifecycle, cfg config.Config) (*awssqs.Client, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, err := sqs.NewClient(ctx, cfg.SQS)
	if err != nil {
		return nil, err
	}
	if err := sqs.Ping(ctx, client, cfg.SQS.IngressQueueURL); err != nil {
		return nil, err
	}
	if err := sqs.Ping(ctx, client, cfg.SQS.EventQueueURL); err != nil {
		return nil, err
	}
	return client, nil
}

func newStore(pool *pgxpool.Pool) *postgres.Store { return postgres.NewStore(pool) }

func newService(store *postgres.Store, clock application.Clock) *application.WagerService {
	return application.NewWagerService(store, clock, application.WithReconciler(store))
}

func newReadiness(pool *pgxpool.Pool, client *awssqs.Client, cfg config.Config) *health.Checker {
	return health.New(
		func(ctx context.Context) error { return pool.Ping(ctx) },
		func(ctx context.Context) error { return sqs.Ping(ctx, client, cfg.SQS.IngressQueueURL) },
	)
}

func newHTTPHandler(cfg config.Config, service *application.WagerService, readiness *health.Checker,
	authenticator middleware.Authenticator, bundle *metrics.Metrics, registry *metrics.Registry, logger *slog.Logger) *httpadapter.Handler {
	return httpadapter.NewHandler(httpadapter.Config{
		UseCases:      service,
		Readiness:     readiness,
		Authenticator: authenticator,
		Metrics:       bundle,
		Registry:      registry,
		Logger:        logger,
		Concurrency:   cfg.HTTP.ConcurrencyLimit,
		MaxBodyBytes:  cfg.HTTP.MaxBodyBytes,
	})
}

func newConsumer(cfg config.Config, client *awssqs.Client, service *application.WagerService,
	bundle *metrics.Metrics, logger *slog.Logger, failpoints *failpoint.Set) *consumer.Consumer {
	return consumer.New(consumer.Config{
		QueueURL:          cfg.SQS.IngressQueueURL,
		ConsumerName:      cfg.SQS.ConsumerName,
		ProviderForSender: cfg.ProviderForSender,
	}, client, service,
		consumer.WithMetrics(bundle),
		consumer.WithLogger(logger),
		consumer.WithFailpoints(failpoints),
	)
}

func newEventSender(client *awssqs.Client, cfg config.Config) *sqs.EventSender {
	return sqs.NewEventSender(client, cfg.SQS.EventQueueURL)
}

func newOutboxPublisher(store *postgres.Store, sender *sqs.EventSender, bundle *metrics.Metrics,
	logger *slog.Logger, failpoints *failpoint.Set) *outbox.Publisher {
	return outbox.New(store, sender,
		outbox.WithMetrics(bundle),
		outbox.WithLogger(logger),
		outbox.WithFailpoints(failpoints),
	)
}

func newReferenceWorker(store *postgres.Store, service *application.WagerService, bundle *metrics.Metrics,
	logger *slog.Logger, failpoints *failpoint.Set) *worker.ReferenceWorker {
	return worker.New(store, service,
		worker.WithMetrics(bundle),
		worker.WithLogger(logger),
		worker.WithFailpoints(failpoints),
	)
}

func registerHTTPServer(lc fx.Lifecycle, cfg config.Config, handler *httpadapter.Handler,
	logger *slog.Logger, recorder Recorder) {
	srv := server.New(cfg.HTTP.Addr, handler.Routes())
	var (
		stop context.CancelFunc
		done chan struct{}
		once sync.Once
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			listener, err := server.Listen(cfg.HTTP.Addr)
			if err != nil {
				return err
			}
			ctx, cancel := context.WithCancel(context.Background())
			stop = cancel
			done = make(chan struct{})
			go func() {
				defer close(done)
				if err := server.Serve(ctx, srv, listener); err != nil {
					logger.Error("http server stopped", "error", err.Error())
				}
			}()
			logger.Info("http server listening", "addr", cfg.HTTP.Addr)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			once.Do(func() {
				if stop != nil {
					stop()
				}
			})
			if done != nil {
				select {
				case <-done:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			logger.Info("http server stopped")
			recorder.Record("http.stopped")
			return nil
		},
	})
}

func registerConsumer(lc fx.Lifecycle, c *consumer.Consumer, logger *slog.Logger, recorder Recorder) {
	registerRunner(lc, "sqs.stopped", logger, recorder, c.Run)
}

func registerOutboxPublisher(lc fx.Lifecycle, p *outbox.Publisher, logger *slog.Logger, recorder Recorder) {
	registerRunner(lc, "outbox.stopped", logger, recorder, p.Run)
}

func registerReferenceWorker(lc fx.Lifecycle, w *worker.ReferenceWorker, logger *slog.Logger, recorder Recorder) {
	registerRunner(lc, "reference.stopped", logger, recorder, w.Run)
}

// registerRunner attaches one long-running worker to the Fx lifecycle: the
// hook starts it in a goroutine and cancels it before shutdown continues.
func registerRunner(lc fx.Lifecycle, stopEvent string, logger *slog.Logger, recorder Recorder, run func(ctx context.Context) error) {
	var (
		stop context.CancelFunc
		done chan struct{}
		once sync.Once
	)
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			ctx, cancel := context.WithCancel(context.Background())
			stop = cancel
			done = make(chan struct{})
			go func() {
				defer close(done)
				if err := run(ctx); err != nil {
					logger.Error("worker stopped", "event", stopEvent, "error", err.Error())
				}
			}()
			return nil
		},
		OnStop: func(ctx context.Context) error {
			once.Do(func() {
				if stop != nil {
					stop()
				}
			})
			if done != nil {
				select {
				case <-done:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			logger.Info("worker stopped", "event", stopEvent)
			recorder.Record(stopEvent)
			return nil
		},
	})
}
