package main

import (
	"fmt"
	"net/http"

	"github.com/gartnera/gofin/internal/auth"
	"github.com/gartnera/gofin/internal/scanner"
	"github.com/gartnera/gofin/internal/server"
	"github.com/spf13/cobra"
)

func serveCmd(loadCfg cfgLoader, openDB dbOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Run the HTTP server",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client, err := openDB(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer client.Close()

			srv := server.New(client, cfg.ServerName)
			fmt.Printf("gofin listening on %s\n", cfg.Listen)
			return http.ListenAndServe(cfg.Listen, srv)
		},
	}
}

func scanCmd(loadCfg cfgLoader, openDB dbOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "scan",
		Short: "Index all configured libraries",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client, err := openDB(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer client.Close()

			sc := scanner.New(client)
			for _, libCfg := range cfg.Libraries {
				lib, err := sc.EnsureLibrary(cmd.Context(), libCfg.Name, libCfg.Type, libCfg.Path)
				if err != nil {
					return fmt.Errorf("library %q: %w", libCfg.Name, err)
				}
				if err := sc.ScanLibrary(cmd.Context(), lib); err != nil {
					return fmt.Errorf("scan %q: %w", libCfg.Name, err)
				}
				fmt.Printf("scanned %q (%s)\n", libCfg.Name, libCfg.Type)
			}
			return nil
		},
	}
}

func userCmd(loadCfg cfgLoader, openDB dbOpener) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "user",
		Short: "Manage users",
	}
	cmd.AddCommand(userAddCmd(loadCfg, openDB))
	return cmd
}

func userAddCmd(loadCfg cfgLoader, openDB dbOpener) *cobra.Command {
	var (
		name     string
		password string
		isAdmin  bool
	)
	c := &cobra.Command{
		Use:   "add",
		Short: "Create a user with a bcrypt-hashed password",
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" || password == "" {
				return fmt.Errorf("--name and --password are required")
			}
			cfg, err := loadCfg()
			if err != nil {
				return err
			}
			client, err := openDB(cmd.Context(), cfg)
			if err != nil {
				return err
			}
			defer client.Close()

			hash, err := auth.HashPassword(password)
			if err != nil {
				return err
			}
			u, err := client.User.Create().
				SetName(name).
				SetPasswordHash(hash).
				SetIsAdmin(isAdmin).
				Save(cmd.Context())
			if err != nil {
				return fmt.Errorf("create user: %w", err)
			}
			fmt.Printf("created user %q (id %s)\n", u.Name, u.ID)
			return nil
		},
	}
	c.Flags().StringVar(&name, "name", "", "username")
	c.Flags().StringVar(&password, "password", "", "password")
	c.Flags().BoolVar(&isAdmin, "admin", false, "grant admin rights")
	return c
}
