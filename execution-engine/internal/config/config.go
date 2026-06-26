package config

type Config struct {
	Port             int    `yaml:"Port"`
	DSN              string `yaml:"DSN"`
	RedisAddr        string `yaml:"RedisAddr"`
	JaegerEndpoint   string `yaml:"JaegerEndpoint"` // host:port, e.g. localhost:4317
	AnthropicAPIKey  string `yaml:"AnthropicAPIKey"`
	OpenAIAPIKey     string `yaml:"OpenAIAPIKey"`
}
