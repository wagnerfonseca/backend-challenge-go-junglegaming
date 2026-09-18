// Command service is the runtime entry point. Configuration is validated
// before the Fx graph starts; startup failures exit non-zero and SIGTERM is
// handled by the Fx run loop, which stops HTTP and workers before closing
// resources.
package main

import (
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/app"
	"github.com/wagnerfonseca/backend-challenge-go-junglegaming/internal/adapters/config"
)

func main() {
	cfg := config.Load()
	application, err := app.New(cfg)
	if err != nil {
		panic(err)
	}
	application.Run()
}
