// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.1

package main

import (
	"context"
	"flag"
	"fmt"

	"api-gateway/internal/config"
	"api-gateway/internal/handler"
	"api-gateway/internal/svc"
	"api-gateway/internal/worker"

	"github.com/zeromicro/go-zero/core/conf"
	"github.com/zeromicro/go-zero/rest"
)

var configFile = flag.String("f", "etc/gateway-api.yaml", "the config file")

func main() {
	flag.Parse()

	var c config.Config
	conf.MustLoad(*configFile, &c)

	server := rest.MustNewServer(c.RestConf)
	defer server.Stop()

	ctx := svc.NewServiceContext(c)
	handler.RegisterHandlers(server, ctx)

	// Start async execution worker pool — consumes from RabbitMQ, dispatches via gRPC.
	w := worker.New(c.RabbitMQ.URL, ctx.DB, ctx.EngineClient, c.RabbitMQ.WorkerCount)
	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	if err := w.Start(workerCtx); err != nil {
		fmt.Printf("WARNING: worker pool failed to start: %v\n", err)
	}

	fmt.Printf("Starting server at %s:%d...\n", c.RestConf.Host, c.RestConf.Port)
	server.Start()
}
