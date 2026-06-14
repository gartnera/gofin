package main

import (
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gartnera/gofin/ent"
	"github.com/gartnera/gofin/internal/auth"
	"github.com/gartnera/gofin/internal/db"
	"github.com/gartnera/gofin/internal/discovery"
	"github.com/gartnera/gofin/internal/metadata/tmdb"
	"github.com/gartnera/gofin/internal/sample"
	"github.com/gartnera/gofin/internal/scanner"
	"github.com/gartnera/gofin/internal/server"
	"github.com/gartnera/gofin/internal/watch"
	"github.com/spf13/cobra"
)

// httpPort extracts the TCP port from a listen address like ":8096" or
// "0.0.0.0:8096" so the discovery responder can advertise it.
func httpPort(listen string) (int, error) {
	_, portStr, err := net.SplitHostPort(listen)
	if err != nil {
		return 0, fmt.Errorf("parse listen address %q: %w", listen, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return 0, fmt.Errorf("parse listen port %q: %w", portStr, err)
	}
	return port, nil
}

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

			// serve owns schema migration so DDL only ever runs from one
			// process; short-lived commands (user add) open without migrating.
			if err := db.Migrate(cmd.Context(), client); err != nil {
				return err
			}

			// One WebSocket hub, shared between the scanner's change hook (which
			// broadcasts LibraryChanged) and the HTTP server's /socket endpoint.
			hub := server.NewSocketHub()

			// Share one scanner between the HTTP refresh endpoints and the
			// filesystem watcher so their index mutations stay serialised. Its
			// change hook drives live LibraryChanged events; because the watcher
			// and refresh endpoints all mutate through the scanner, this one hook
			// observes every change.
			scOpts := []scanner.Option{scanner.WithChangeHook(hub.NotifyLibraryChanged)}
			if cfg.Metadata.Enabled {
				scOpts = append(scOpts,
					scanner.WithMetadataProvider(tmdb.New(cfg.Metadata.TMDbToken)),
					scanner.WithImageCacheDir(cfg.Metadata.CacheDir),
					scanner.WithMetadataTTL(time.Duration(cfg.Metadata.TTLDays)*24*time.Hour),
				)
			}
			sc := scanner.New(client, scOpts...)
			libs := make([]*ent.Library, 0, len(cfg.Libraries))
			for _, libCfg := range cfg.Libraries {
				lib, err := sc.EnsureLibrary(cmd.Context(), libCfg.Name, libCfg.Type, libCfg.Path)
				if err != nil {
					return fmt.Errorf("library %q: %w", libCfg.Name, err)
				}
				// Initial scan picks up changes since the last run; unchanged
				// files are skipped via the mtime/size check.
				if err := sc.ScanLibrary(cmd.Context(), lib); err != nil {
					return fmt.Errorf("scan %q: %w", libCfg.Name, err)
				}
				libs = append(libs, lib)
			}

			// Background remote-metadata enricher (no-op unless configured). It
			// runs an initial sweep of un-enriched items and then drains the
			// queue the scanner/watcher feed it.
			sc.StartEnricher(cmd.Context())

			w, err := watch.New(sc, libs,
				watch.WithWatchWindow(time.Duration(cfg.WatchWindowDays())*24*time.Hour),
				watch.WithRescanInterval(time.Duration(cfg.WatchRescanHours())*time.Hour),
			)
			if err != nil {
				return fmt.Errorf("start watcher: %w", err)
			}
			go func() {
				if err := w.Run(cmd.Context()); err != nil && cmd.Context().Err() == nil {
					fmt.Printf("watcher stopped: %v\n", err)
				}
			}()

			// UDP client auto-discovery (port 7359), so stock clients find gofin
			// on the LAN without typing its address. Enabled by default; a bind
			// failure is logged but never fatal to the HTTP server.
			if cfg.DiscoveryEnabled() {
				if port, err := httpPort(cfg.Listen); err != nil {
					fmt.Printf("discovery disabled: %v\n", err)
				} else {
					disco := discovery.New(server.DeriveServerID(cfg.ServerName), cfg.ServerName, port)
					go func() {
						if err := disco.ListenAndServe(cmd.Context()); err != nil && cmd.Context().Err() == nil {
							fmt.Printf("discovery stopped: %v\n", err)
						}
					}()
				}
			}

			opts := []server.Option{
				server.WithScanner(sc),
				server.WithQuickConnect(cfg.QuickConnectEnabled()),
				server.WithHub(hub),
			}
			if cfg.WebRoot != "" {
				opts = append(opts, server.WithWebRoot(cfg.WebRoot))
			}
			srv := server.New(client, cfg.ServerName, opts...)
			fmt.Printf("gofin listening on %s\n", cfg.Listen)
			return http.ListenAndServe(cfg.Listen, srv)
		},
	}
}

func migrateCmd(loadCfg cfgLoader, openDB dbOpener) *cobra.Command {
	return &cobra.Command{
		Use:   "migrate",
		Short: "Create or update the database schema",
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

			if err := db.Migrate(cmd.Context(), client); err != nil {
				return err
			}
			fmt.Println("schema up to date")
			return nil
		},
	}
}

func sampleCmd() *cobra.Command {
	opts := sample.Options{
		Movies:            10000,
		Series:            500,
		Seasons:           2,
		EpisodesPerSeason: 10,
	}
	var dir string
	c := &cobra.Command{
		Use:   "sample",
		Short: "Generate a large synthetic media library for benchmarking",
		Long: "Writes media files with realistic names and directory layouts under\n" +
			"<dir>/{movies,tv,music}. By default the files are empty placeholders —\n" +
			"they exist to exercise scanning and querying at scale, and are not\n" +
			"playable. With --real, a few base files are encoded once via ffmpeg and\n" +
			"every entry is symlinked to one, so the whole library direct-plays in a\n" +
			"browser (requires ffmpeg on PATH).",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := sample.Generate(dir, opts)
			if err != nil {
				return err
			}
			fmt.Printf("generated under %s: %d movies, %d episodes, %d tracks\n",
				dir, res.Movies, res.Episodes, res.Tracks)
			fmt.Println("\nPoint gofin.yaml at the generated folders, e.g.:")
			fmt.Println("  libraries:")
			if res.Movies > 0 {
				fmt.Printf("    - { name: Movies, type: movies, path: %s }\n", res.MoviesDir)
			}
			if res.Episodes > 0 {
				fmt.Printf("    - { name: TV Shows, type: tvshows, path: %s }\n", res.TVDir)
			}
			if res.Tracks > 0 {
				fmt.Printf("    - { name: Music, type: music, path: %s }\n", res.MusicDir)
			}
			return nil
		},
	}
	c.Flags().StringVar(&dir, "dir", "./sample-large", "output directory")
	c.Flags().IntVar(&opts.Movies, "movies", opts.Movies, "number of movies")
	c.Flags().IntVar(&opts.Series, "series", opts.Series, "number of TV series")
	c.Flags().IntVar(&opts.Seasons, "seasons", opts.Seasons, "seasons per series")
	c.Flags().IntVar(&opts.EpisodesPerSeason, "episodes-per-season", opts.EpisodesPerSeason, "episodes per season")
	c.Flags().IntVar(&opts.Artists, "artists", opts.Artists, "number of music artists (0 disables)")
	c.Flags().IntVar(&opts.AlbumsPerArtist, "albums-per-artist", opts.AlbumsPerArtist, "albums per artist")
	c.Flags().IntVar(&opts.TracksPerAlbum, "tracks-per-album", opts.TracksPerAlbum, "tracks per album")
	c.Flags().BoolVar(&opts.Real, "real", false, "generate real, direct-playable media (symlinks to ffmpeg-encoded base files; requires ffmpeg)")
	c.Flags().IntVar(&opts.RealBase, "real-base", 3, "with --real, number of distinct base files to encode per media type")
	return c
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
				// user add never migrates; a missing schema means the database
				// hasn't been initialised yet.
				if strings.Contains(err.Error(), "no such table") {
					return fmt.Errorf("database not initialised: run `gofin migrate` (or `gofin serve`) first")
				}
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
