// Package main is the entry point for the hostcheck service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"syscall"
	"time"

	"github.com/dosquad/go-cliversion"
	"github.com/na4ma4/config"
	"github.com/na4ma4/go-contextual"
	"github.com/na4ma4/go-healthcheck"
	"github.com/na4ma4/go-slogtool"
	"github.com/na4ma4/hostcheck/internal/plugin"
	"github.com/na4ma4/hostcheck/internal/server"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:     "hostcheck",
	Short:   "Plugin-based host checking web service",
	RunE:    mainCommand,
	Version: cliversion.Get().VersionString(),
}

const (
	defaultRequestLimit = 10                // Default rate limit in requests per second
	defaultMaxTimeout   = 300 * time.Second // Default maximum timeout per request
)

func init() {
	cobra.OnInitialize(configInit)

	rootCmd.PersistentFlags().StringP("listen", "l", "127.0.0.1:8080", "Listen address")
	_ = viper.BindPFlag("server.listen", rootCmd.PersistentFlags().Lookup("listen"))
	_ = viper.BindEnv("server.listen", "LISTEN")

	rootCmd.PersistentFlags().StringP("plugins", "p", "./plugins", "Plugin directory")
	_ = viper.BindPFlag("plugins.directory", rootCmd.PersistentFlags().Lookup("plugins"))
	_ = viper.BindEnv("plugins.directory", "PLUGINS_DIR")

	rootCmd.PersistentFlags().BoolP("debug", "d", false, "Enable debug logging")
	_ = viper.BindPFlag("debug", rootCmd.PersistentFlags().Lookup("debug"))
	_ = viper.BindEnv("debug", "DEBUG")

	rootCmd.PersistentFlags().Float64("rate-limit", defaultRequestLimit, "Rate limit in requests per second")
	_ = viper.BindPFlag("server.rate_limit", rootCmd.PersistentFlags().Lookup("rate-limit"))
	_ = viper.BindEnv("server.rate_limit", "RATE_LIMIT")

	rootCmd.PersistentFlags().Int("max-concurrent", 0, "Max concurrent checks (0=auto: max(4, NumCPU))")
	_ = viper.BindPFlag("server.max_concurrent", rootCmd.PersistentFlags().Lookup("max-concurrent"))
	_ = viper.BindEnv("server.max_concurrent", "MAX_CONCURRENT")

	rootCmd.PersistentFlags().Duration("max-timeout", defaultMaxTimeout,
		"Maximum allowed timeout per request (e.g., 30s, 5m)")
	_ = viper.BindPFlag("server.max_timeout", rootCmd.PersistentFlags().Lookup("max-timeout"))
	_ = viper.BindEnv("server.max_timeout", "MAX_TIMEOUT")
}

func configInit() {
	viper.SetConfigName("hostcheck")
	viper.SetConfigType("yaml")
	viper.AddConfigPath(".")
	viper.AddConfigPath("/etc/hostcheck")
	viper.AddConfigPath("$HOME/.hostcheck")

	_ = viper.ReadInConfig()
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		// _, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func mainCommand(_ *cobra.Command, _ []string) error {
	cfg := config.NewViperConfigFromViper(viper.GetViper(), "hostcheck")

	// Create LogManager for runtime-configurable log levels
	logmgr := slogtool.NewSlogManager(
		slogtool.WithDefaultLevel(func() slog.Level {
			if cfg.GetBool("debug") {
				return slog.LevelDebug
			}
			return slog.LevelInfo
		}()),
		slogtool.WithJSONHandler(),
		slogtool.WithWriter(os.Stderr),
	)

	logger := logmgr.Named("hostcheck")
	slog.SetDefault(logger)

	logger.Debug("starting hostcheck service", "debug", cfg.GetBool("debug"))

	// Validate configuration
	listenAddr := cfg.GetString("server.listen")
	if listenAddr == "" {
		logger.Error("server.listen is required")
		return errors.New("server.listen is required")
	}

	ctx := contextual.New(context.Background(),
		contextual.WithSignalCancelOption(os.Interrupt, syscall.SIGTERM),
	)
	defer ctx.Cancel()

	hc := healthcheck.NewCore()

	registry := plugin.NewRegistry(logger)

	// Load per-plugin configurations
	loadPluginConfigs(registry)

	pluginDir := cfg.GetString("plugins.directory")
	logger.Debug("loading plugins from directory", "directory", pluginDir)

	if err := registry.LoadDirectory(pluginDir); err != nil {
		if errors.Is(err, plugin.ErrNoPluginsDirectory) {
			logger.Error("plugins directory does not exist", "directory", pluginDir)
			return fmt.Errorf("plugins directory does not exist: %s", pluginDir)
		}
		if errors.Is(err, plugin.ErrNoPluginsLoaded) {
			logger.Error("no plugins loaded from directory", "directory", pluginDir)
			return fmt.Errorf("no plugins loaded from directory: %s", pluginDir)
		}
		logger.Error("failed to load plugins directory", "directory", pluginDir, "error", err)
		return fmt.Errorf("failed to load plugins: %w", err)
	}

	pluginNames := registry.Names()
	logger.Debug("loaded plugins", "count", len(pluginNames), "names", pluginNames)

	// Create server config
	serverCfg := server.Config{
		RateLimit:     cfg.GetFloat64("server.rate_limit"),
		MaxConcurrent: cfg.GetInt("server.max_concurrent"),
		MaxTimeout:    cfg.GetDuration("server.max_timeout"),
	}

	srv := server.NewServer(registry, logger, hc, serverCfg, logmgr)
	logger.Debug("starting server", "listen", listenAddr,
		"rate_limit", serverCfg.RateLimit,
		"max_concurrent", serverCfg.MaxConcurrent,
		"max_timeout", serverCfg.MaxTimeout)

	if err := srv.Run(ctx, listenAddr); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}

	return nil
}

func loadPluginConfigs(registry *plugin.Registry) {
	// Get all plugin-related config keys
	pluginsCfg := viper.Sub("plugins")
	if pluginsCfg == nil {
		return
	}

	// Iterate over plugin-specific configurations (e.g., plugins.dns.timeout)
	for key := range pluginsCfg.AllSettings() {
		if key == "directory" {
			continue
		}

		sub := pluginsCfg.Sub(key)
		if sub != nil {
			cfgMap := sub.AllSettings()
			if len(cfgMap) > 0 {
				registry.SetConfig(key, cfgMap)
			}
		}
	}
}
