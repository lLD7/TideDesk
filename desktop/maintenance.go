package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"gammagrid.local/desktop/internal/provider"
	"gammagrid.local/desktop/internal/store"
)

func runMaintenance(args []string) error {
	flags := flag.NewFlagSet("maintenance", flag.ContinueOnError)
	root := flags.String("root", "..", "TideDesk source directory")
	cwd := flags.String("cwd", ".", "base for relative paths")
	database := flags.String("database", os.Getenv("GEX_DATABASE_PATH"), "database path")
	source := flags.String("provider", os.Getenv("GEX_PROVIDER"), "auto, cboe, or yahoo")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: scripts/maintenance.sh [flags] backup DEST.db | collect SYMBOL...")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	command := flags.Arg(0)
	switch command {
	case "backup":
		if flags.NArg() != 2 {
			return fmt.Errorf("backup needs a destination")
		}
	case "collect":
		if flags.NArg() < 2 {
			return fmt.Errorf("collect needs at least one symbol")
		}
		switch *source {
		case "", "auto", "cboe", "yahoo":
		default:
			return fmt.Errorf("invalid provider %q", *source)
		}
	default:
		flags.Usage()
		return flag.ErrHelp
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(*cwd, path)
	}
	rootPath, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	path := filepath.Join(rootPath, "data", "options.db")
	if *database != "" {
		path = resolve(*database)
	}
	if command == "backup" {
		if _, err := os.Stat(path); err != nil {
			return err
		}
	}
	databaseStore, err := store.Open(rootPath, path)
	if err != nil {
		return err
	}
	defer databaseStore.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if command == "backup" {
		dest := resolve(flags.Arg(1))
		if err := databaseStore.Backup(ctx, dest); err != nil {
			return err
		}
		fmt.Println("Backup saved:", dest)
		return nil
	}
	if err := os.Setenv("GEX_PROVIDER", *source); err != nil {
		return err
	}
	symbols := make([]string, 0, flags.NArg()-1)
	for _, raw := range flags.Args()[1:] {
		symbol, err := provider.NormalizeSymbol(raw)
		if err != nil {
			return err
		}
		symbols = append(symbols, symbol)
	}
	if err := databaseStore.Transaction(ctx, func(tx *sql.Tx) error {
		for _, symbol := range symbols {
			if _, err := tx.ExecContext(ctx, "INSERT INTO watchlist(ticker) VALUES(?) ON CONFLICT DO NOTHING", symbol); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	service := newDesktopService(databaseStore, rootPath)
	defer service.Close()
	var failed bool
	for _, symbol := range symbols {
		results, err := service.Collect(ctx, symbol)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", symbol, results[symbol])
		failed = failed || results[symbol] != "success"
	}
	if failed {
		return fmt.Errorf("one or more collections failed")
	}
	return nil
}
