package infra

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/swagger"
	"github.com/patricksign/AgentClaw/config"
	"github.com/patricksign/AgentClaw/internal/middleware"
	"github.com/redis/go-redis/v9"
)

type HttpServer struct {
	AppName string
	Conf    *config.ServerInfo
	CORS    *config.MiddlewareConfig
	Redis   *redis.Client
	RedisCf *config.RedisConfig
	app     *fiber.App
}

// Start launches the HTTP listener in a background goroutine.
// Returns a channel that receives the first error from Listen (or is closed on clean shutdown).
func (r *HttpServer) Start() <-chan error {
	errCh := make(chan error, 1)
	if r == nil || r.app == nil {
		errCh <- fmt.Errorf("http server is nil")
		return errCh
	}

	go func() {
		addr := fmt.Sprintf("%s:%d", r.Conf.Host, r.Conf.Port)
		slog.Info("API server listening", "addr", addr)
		if err := r.app.Listen(addr); err != nil {
			errCh <- err
		}
		close(errCh)
	}()
	return errCh
}

func (r *HttpServer) InitHttpServer() {
	app := fiber.New(r.ConfigFiber(r.Conf))

	// Request logger first — captures all requests including rejected ones
	r.app = app
	r.SetupPrintAPIRoutes()

	// Security headers (first layer of defense)
	app.Use(middleware.SecureHeadersMiddleware(middleware.DefaultSecurityHeadersConfig))

	// Request ID tracking
	app.Use(middleware.RequestIDMiddleware(middleware.DefaultRequestIDConfig))

	// Apply CORS middleware with configuration
	app.Use(middleware.CorsFilter(r.CORS.CORS))

	// Apply general rate limiting to all API endpoints
	if r.CORS.RateLimit.Enabled {
		var redisClient *redis.Client
		if r.CORS.RateLimit.UseRedis && r.Redis != nil {
			redisClient = r.Redis
		}
		app.Use(middleware.RateLimitFilter(r.CORS.RateLimit, r.RedisCf, redisClient))
	}

	r.SetupSwagger()
}

func (r *HttpServer) SetupPrintAPIRoutes() {
	r.app.Use(logger.New(logger.Config{
		Format:     "[${time}] ${status} - ${method} ${path} ${latency}\n",
		TimeFormat: "02/01/2006 15:04:05",
		TimeZone:   "Asia/Ho_Chi_Minh",
		// Only log API routes, skip static assets and health checks
		Next: func(c *fiber.Ctx) bool {
			return r.ignorePath(c.Path())
		},
	}))
}

func (r *HttpServer) ignorePath(path string) bool {
	return path == "/" ||
		path == "/favicon.ico" ||
		path == "/healthz" ||
		path == "/metrics" ||
		strings.HasPrefix(path, "/static/") ||
		strings.HasPrefix(path, "/public/")
}

func (r *HttpServer) SetupSwagger() {
	r.app.Get("/swagger/*", swagger.New(swagger.Config{
		Title:        "Base Service API Documentation",
		DeepLinking:  true,
		DocExpansion: "none",
	}))
}

func (r *HttpServer) App() *fiber.App {
	return r.app
}

func (r *HttpServer) ConfigFiber(conf *config.ServerInfo) fiber.Config {
	return fiber.Config{
		AppName:               conf.AppName,
		EnablePrintRoutes:     false, // Disabled - we'll print only API routes manually
		DisableStartupMessage: false,
		ReadTimeout:           time.Duration(conf.ReadTimeout) * time.Second,
		WriteTimeout:          time.Duration(conf.WriteTimeout) * time.Second,
	}
}
