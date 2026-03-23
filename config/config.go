package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"log/slog"
	"github.com/spf13/viper"
)

type Config struct {
	Profile    string           `mapstructure:"profile" json:"profile,omitempty"`
	Server     ServerConfig     `mapstructure:"server" json:"server,omitempty"`
	Database   DatabaseConfig   `mapstructure:"database" json:"database,omitempty"`
	Redis      RedisConfig      `mapstructure:"redis" json:"redis,omitempty"`
	Neo4j      Neo4jConfig      `mapstructure:"neo4j" json:"neo4j,omitempty"`
	Middleware MiddlewareConfig `mapstructure:"middleware" json:"middleware,omitempty"`
	LLM        LLMConfig        `mapstructure:"llm" json:"llm,omitempty"`
	Telegram   TelegramConfig   `mapstructure:"telegram" json:"telegram,omitempty"`
	GitHub     GitHubConfig     `mapstructure:"github" json:"github,omitempty"`
	Trello     TrelloConfig     `mapstructure:"trello" json:"trello,omitempty"`
	Slack      SlackConfig      `mapstructure:"slack" json:"slack,omitempty"`
	Paths      PathsConfig      `mapstructure:"paths" json:"paths,omitempty"`
	Admin      AdminConfig      `mapstructure:"admin" json:"admin,omitempty"`
}

type LLMConfig struct {
	AnthropicAPIKey string `mapstructure:"anthropicApiKey" json:"anthropic_api_key,omitempty"`
	MinimaxAPIKey   string `mapstructure:"minimaxApiKey" json:"minimax_api_key,omitempty"`
	KimiAPIKey      string `mapstructure:"kimiApiKey" json:"kimi_api_key,omitempty"`
	GLMAPIKey       string `mapstructure:"glmApiKey" json:"glm_api_key,omitempty"`
}

type TelegramConfig struct {
	BotToken     string `mapstructure:"botToken" json:"bot_token,omitempty"`
	StatusChatID string `mapstructure:"statusChatId" json:"status_chat_id,omitempty"`
	HumanChatID  string `mapstructure:"humanChatId" json:"human_chat_id,omitempty"`
}

type GitHubConfig struct {
	Token string `mapstructure:"token" json:"token,omitempty"`
	Owner string `mapstructure:"owner" json:"owner,omitempty"`
	Repo  string `mapstructure:"repo" json:"repo,omitempty"`
}

type TrelloConfig struct {
	APIKey      string `mapstructure:"apiKey" json:"api_key,omitempty"`
	Token       string `mapstructure:"token" json:"token,omitempty"`
	IdeaBoardID string `mapstructure:"ideaBoardId" json:"idea_board_id,omitempty"`
	DoneListID  string `mapstructure:"doneListId" json:"done_list_id,omitempty"`
	ListID      string `mapstructure:"listId" json:"list_id,omitempty"`
}

type SlackConfig struct {
	WebhookURL string `mapstructure:"webhookUrl" json:"webhook_url,omitempty"`
}

type AdminConfig struct {
	Token      string `mapstructure:"token" json:"token,omitempty"`
	CORSOrigin string `mapstructure:"corsOrigin" json:"cors_origin,omitempty"`
}

type PathsConfig struct {
	Addr         string `mapstructure:"addr" json:"addr,omitempty"`
	DBPath       string `mapstructure:"dbPath" json:"db_path,omitempty"`
	ProjectPath  string `mapstructure:"projectPath" json:"project_path,omitempty"`
	StatePath    string `mapstructure:"statePath" json:"state_path,omitempty"`
	PricingPath  string `mapstructure:"pricingPath" json:"pricing_path,omitempty"`
	AgentsConfig string `mapstructure:"agentsConfig" json:"agents_config,omitempty"`
}

type ServerConfig struct {
	Http ServerInfo `mapstructure:"http" json:"http,omitempty"`
	Grpc ServerInfo `mapstructure:"grpc" json:"grpc,omitempty"`
}

type ServerInfo struct {
	AppName        string `mapstructure:"appName" json:"app_name,omitempty"`
	Host           string `mapstructure:"host" json:"host,omitempty"`
	Port           int    `mapstructure:"port" json:"port,omitempty"`
	EnableTLS      bool   `mapstructure:"enableTLS" json:"enable_tls,omitempty"`
	ReadTimeout    int    `mapstructure:"readTimeout" json:"read_timeout,omitempty"`
	WriteTimeout   int    `mapstructure:"writeTimeout" json:"write_timeout,omitempty"`
	ConnectTimeOut int    `mapstructure:"connectTimeOut" json:"connect_time_out,omitempty"`
}

type DatabaseConfig struct {
	DriverName         string        `mapstructure:"driverName" json:"driver_name,omitempty"`
	Host               string        `mapstructure:"host" json:"host,omitempty"`
	Port               int           `mapstructure:"port" json:"port,omitempty"`
	UserName           string        `mapstructure:"userName" json:"user_name,omitempty"`
	Password           string        `mapstructure:"password" json:"password,omitempty"`
	DBName             string        `mapstructure:"dbName" json:"db_name,omitempty"`
	SSLMode            string        `mapstructure:"sslMode" json:"ssl_mode,omitempty"`
	MaxOpenConnections int32         `mapstructure:"maxOpenConnections" json:"max_open_connections,omitempty"`
	MaxIdleConnections int32         `mapstructure:"maxIdleConnections" json:"max_idle_connections,omitempty"`
	MaxConnLifetime    time.Duration `mapstructure:"maxConnLifetime" json:"max_conn_lifetime,omitempty"`
	MaxConnIdleTime    time.Duration `mapstructure:"maxConnIdleTime" json:"max_conn_idle_time,omitempty"`
}

type RedisConfig struct {
	Host         string        `mapstructure:"host" json:"host,omitempty"`
	Port         int           `mapstructure:"port" json:"port,omitempty"`
	Password     string        `mapstructure:"password" json:"password,omitempty"`
	DB           int           `mapstructure:"db" json:"db,omitempty"`
	MaxIdle      int           `mapstructure:"maxIdle" json:"max_idle,omitempty"`
	MinIdle      int           `mapstructure:"minIdle" json:"min_idle,omitempty"`
	DialTimeout  time.Duration `mapstructure:"dialTimeout" json:"dial_timeout,omitempty"`
	ReadTimeout  time.Duration `mapstructure:"readTimeout" json:"read_timeout,omitempty"`
	WriteTimeout time.Duration `mapstructure:"writeTimeout" json:"write_timeout,omitempty"`
}

type Neo4jConfig struct {
	Host         string        `mapstructure:"host" json:"host,omitempty"`
	Port         int           `mapstructure:"port" json:"port,omitempty"`
	UserName     string        `mapstructure:"username" json:"username,omitempty"`
	Password     string        `mapstructure:"password" json:"password,omitempty"`
	DBName       string        `mapstructure:"dbName" json:"db_name,omitempty"`
	MaxOpenConns int32         `mapstructure:"maxOpenConnections" json:"max_open_connections,omitempty"`
	MaxIdleConns int32         `mapstructure:"maxIdleConnections" json:"max_idle_connections,omitempty"`
	MaxConnLife  time.Duration `mapstructure:"maxConnLifetime" json:"max_conn_lifetime,omitempty"`
	MaxConnIdle  time.Duration `mapstructure:"maxConnIdleTime" json:"max_conn_idle_time,omitempty"`
}

type MiddlewareConfig struct {
	Token     TokenConfig     `mapstructure:"token" json:"token,omitempty"`
	CORS      CORSConfig      `mapstructure:"cors" json:"cors,omitempty"`
	RateLimit RateLimitConfig `mapstructure:"rateLimit" json:"rate_limit,omitempty"`
}

type TokenConfig struct {
	PasswordSalt       string        `mapstructure:"passwordSalt" json:"password_salt,omitempty"`
	AccessTokenSecret  string        `mapstructure:"accessTokenSecret" json:"access_token_secret,omitempty"`
	AccessTokenExp     time.Duration `mapstructure:"accessTokenExp" json:"access_token_exp,omitempty"`
	RefreshTokenSecret string        `mapstructure:"refreshTokenSecret" json:"refresh_token_secret,omitempty"`
	RefreshTokenExp    time.Duration `mapstructure:"refreshTokenExp" json:"refresh_token_exp,omitempty"`
}

type CORSConfig struct {
	AllowedOrigins   []string `mapstructure:"allowedOrigins" json:"allowed_origins,omitempty"`
	AllowedMethods   []string `mapstructure:"allowedMethods" json:"allowed_methods,omitempty"`
	AllowedHeaders   []string `mapstructure:"allowedHeaders" json:"allowed_headers,omitempty"`
	ExposedHeaders   []string `mapstructure:"exposedHeaders" json:"exposed_headers,omitempty"`
	AllowCredentials bool     `mapstructure:"allowCredentials" json:"allow_credentials,omitempty"`
	MaxAge           int      `mapstructure:"maxAge" json:"max_age,omitempty"`
}

type RateLimitConfig struct {
	Enabled        bool          `mapstructure:"enabled" json:"enabled,omitempty"`
	Max            int           `mapstructure:"max" json:"max,omitempty"`                         // Max requests per window
	Expiration     time.Duration `mapstructure:"expiration" json:"expiration,omitempty"`           // Time window (e.g., 1 minute)
	SkipFailedReq  bool          `mapstructure:"skipFailedReq" json:"skip_failed_req,omitempty"`   // Skip failed requests
	SkipSuccessReq bool          `mapstructure:"skipSuccessReq" json:"skip_success_req,omitempty"` // Skip successful requests
	LimitReached   string        `mapstructure:"limitReached" json:"limit_reached,omitempty"`      // Custom message when limit is reached

	// Auth-specific rate limits (stricter)
	AuthEnabled    bool          `mapstructure:"authEnabled" json:"auth_enabled,omitempty"`
	AuthMax        int           `mapstructure:"authMax" json:"auth_max,omitempty"`
	AuthExpiration time.Duration `mapstructure:"authExpiration" json:"auth_expiration,omitempty"`

	// Distributed rate limiting (Redis storage)
	UseRedis bool `mapstructure:"useRedis" json:"use_redis,omitempty"` // Use Redis for distributed rate limiting
	RedisDB  int  `mapstructure:"redisDB" json:"redis_db,omitempty"`   // Redis database number for rate limiting
}

// envBindings maps flat APP_ env vars to viper config keys.
var envBindings = map[string]string{
	// LLM API keys
	"APP_ANTHROPIC_API_KEY": "llm.anthropicApiKey",
	"APP_MINIMAX_API_KEY":   "llm.minimaxApiKey",
	"APP_KIMI_API_KEY":      "llm.kimiApiKey",
	"APP_GLM_API_KEY":       "llm.glmApiKey",
	// Telegram
	"APP_TELEGRAM_BOT_TOKEN":      "telegram.botToken",
	"APP_TELEGRAM_STATUS_CHAT_ID": "telegram.statusChatId",
	"APP_TELEGRAM_HUMAN_CHAT_ID":  "telegram.humanChatId",
	// GitHub
	"APP_GITHUB_TOKEN": "github.token",
	"APP_GITHUB_OWNER": "github.owner",
	"APP_GITHUB_REPO":  "github.repo",
	// Trello
	"APP_TRELLO_API_KEY":       "trello.apiKey",
	"APP_TRELLO_TOKEN":         "trello.token",
	"APP_TRELLO_IDEA_BOARD_ID": "trello.ideaBoardId",
	"APP_TRELLO_DONE_LIST_ID":  "trello.doneListId",
	"APP_TRELLO_LIST_ID":       "trello.listId",
	// Slack
	"APP_SLACK_WEBHOOK_URL": "slack.webhookUrl",
	// Security
	"APP_ADMIN_TOKEN": "admin.token",
	"APP_CORS_ORIGIN": "admin.corsOrigin",
	// Paths (no APP_ prefix in .env, but support both)
	"ADDR":          "paths.addr",
	"DB_PATH":       "paths.dbPath",
	"PROJECT_PATH":  "paths.projectPath",
	"STATE_PATH":    "paths.statePath",
	"PRICING_PATH":  "paths.pricingPath",
	"AGENTS_CONFIG": "paths.agentsConfig",
}

func bindEnvVars(v *viper.Viper) {
	for envKey, viperKey := range envBindings {
		if val := os.Getenv(envKey); val != "" {
			v.Set(viperKey, val)
		}
	}
}

func loadConfig(configfile string) (*viper.Viper, error) {
	v := viper.New()

	// Set defaults for paths.
	v.SetDefault("paths.addr", ":8080")
	v.SetDefault("paths.dbPath", "./AgentClaw.db")
	v.SetDefault("paths.projectPath", "./memory/project.md")
	v.SetDefault("paths.statePath", "./state")
	v.SetDefault("paths.pricingPath", "./pricing/agent-pricing.json")
	v.SetDefault("paths.agentsConfig", "./config/agents.json")

	v.SetConfigFile(configfile)
	v.SetConfigType("yaml")

	v.SetEnvPrefix("APP")
	v.AutomaticEnv()

	v.SetEnvKeyReplacer(strings.NewReplacer("_", "."))

	if err := v.ReadInConfig(); err != nil {
		slog.Warn("config file not found — using environment variables only", "err", err)
	}
	overrideconfig(v)
	bindEnvVars(v)
	return v, nil
}

func overrideconfig(v *viper.Viper) {
	for _, key := range v.AllKeys() {
		envKey := "APP_" + strings.ReplaceAll(strings.ToUpper(key), ".", "_")
		envValue := os.Getenv(envKey)
		if envValue != "" {
			v.Set(key, envValue)
		}

	}
}

func LoadConfig(pathToFile string, env string, config any) error {
	slog.Info("Load config", "env", env)
	configPath := pathToFile + "/" + "application"
	if len(env) > 0 && env != "local" {
		configPath = configPath + "-" + env
	}

	pwd, err := os.Getwd()
	if err == nil && len(pwd) > 0 {
		configPath = pwd + "/" + configPath
	}

	confFile := fmt.Sprintf("%s.yaml", configPath)
	v, loadErr := loadConfig(confFile)
	if loadErr != nil {
		slog.Warn("proceeding with environment variables only", "err", loadErr)
	}

	if err := v.Unmarshal(&config); err != nil {
		return fmt.Errorf("unable to decode into struct, %v", err)
	}
	return nil
}

func (r *DatabaseConfig) BuildConnectionStringPostgres() string {
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		r.Host, r.Port, r.UserName, r.Password, r.DBName, func() string {
			if len(r.SSLMode) == 0 {
				return "disable"
			}
			return r.SSLMode
		}(),
	)
}

func (r *RedisConfig) BuildRedisConnectionString() string {
	if r.Password != "" {
		return fmt.Sprintf("redis://default:%s@%s:%d/%d", r.Password, r.Host, r.Port, r.DB)
	}
	return fmt.Sprintf("redis://%s:%d/%d", r.Host, r.Port, r.DB)
}

func (c *CORSConfig) GetOriginsString() string {
	if len(c.AllowedOrigins) == 0 {
		return "*"
	}
	return strings.Join(c.AllowedOrigins, ", ")
}

func (c *CORSConfig) GetMethodsString() string {
	if len(c.AllowedMethods) == 0 {
		return "GET, POST, PUT, DELETE, OPTIONS"
	}
	return strings.Join(c.AllowedMethods, ", ")
}

func (c *CORSConfig) GetHeadersString() string {
	if len(c.AllowedHeaders) == 0 {
		return "Origin, Content-Type, Accept, Authorization"
	}
	return strings.Join(c.AllowedHeaders, ", ")
}

func (c *CORSConfig) GetExposedHeadersString() string {
	return strings.Join(c.ExposedHeaders, ", ")
}
