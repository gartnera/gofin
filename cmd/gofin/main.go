package main

import (
	"context"
	"fmt"
	"os"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/internal/config"
	"github.com/gartnera/gofin/internal/db"
	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	var configPath string

	root := &cobra.Command{
		Use:   "gofin",
		Short: "gofin is a minimal Jellyfin-compatible media server",
	}

	defaultConfig := os.Getenv("GOFIN_CONFIG")
	if defaultConfig == "" {
		defaultConfig = "gofin.yaml"
	}
	root.PersistentFlags().StringVar(&configPath, "config", defaultConfig, "path to gofin.yaml config file")

	loadCfg := func() (*config.Config, error) { return config.Load(configPath) }
	openDB := func(ctx context.Context, cfg *config.Config) (*ent.Client, error) {
		return db.Open(ctx, cfg.Database)
	}

	root.AddCommand(serveCmd(loadCfg, openDB))
	root.AddCommand(migrateCmd(loadCfg, openDB))
	root.AddCommand(userCmd(loadCfg, openDB))
	return root
}

type (
	cfgLoader func() (*config.Config, error)
	dbOpener  func(context.Context, *config.Config) (*ent.Client, error)
)
