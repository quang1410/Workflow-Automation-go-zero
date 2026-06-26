// Code scaffolded by goctl. Safe to edit.
// goctl 1.10.1

package config

import "github.com/zeromicro/go-zero/rest"

type Config struct {
	rest.RestConf
	DB       DBConfig
	Redis    RedisConf
	Auth     AuthConfig
	RabbitMQ RabbitMQConfig
	Engine   EngineConfig
}

type DBConfig struct {
	DSN string
}

type RedisConf struct {
	Addr string
}

type AuthConfig struct {
	JWKSUrl     string // Keycloak JWKS endpoint for token validation
	KeycloakURL string // Base URL for proxying login requests
}

type RabbitMQConfig struct {
	URL         string // amqp://user:pass@host:5672/
	WorkerCount int    // number of consumer goroutines
}

type EngineConfig struct {
	Addr string // host:port of execution-engine gRPC server
}
