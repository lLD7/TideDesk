package store

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
)

const legacySnapshotRequired = "ticker collected_at underlying_price expiry strike option_type"
const legacySnapshotOptional = "last_price bid ask volume open_interest implied_volatility in_the_money delta gamma theta vega source contract_symbol quote_timestamp"

type importTable struct {
	name, required, optional string
}

var importTables = []importTable{
	{"watchlist", "ticker", "added_at"},
	{"option_snapshots", legacySnapshotRequired, legacySnapshotOptional},
	{"option_snapshots_archive", legacySnapshotRequired, legacySnapshotOptional},
	{"collection_runs", "started_at ticker status", "finished_at error_message rows_fetched oi_zero_fraction source"},
	{"tracked_contracts", "ticker expiry strike option_type", "added_at"},
}

func RunImport(args []string) error {
	flags := flag.NewFlagSet("import-sqlite", flag.ContinueOnError)
	root := flags.String("root", "..", "TideDesk source directory")
	cwd := flags.String("cwd", ".", "base for relative database paths")
	target := flags.String("target", os.Getenv("GEX_DATABASE_PATH"), "new destination database")
	dryRun := flags.Bool("dry-run", false, "count source rows without writing")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: scripts/import-sqlite.sh [--dry-run | --target NEW.db] [SOURCE.db]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 1 {
		return fmt.Errorf("expected at most one source database path")
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(*cwd, path)
	}
	source := filepath.Join(*root, "data", "options.db")
	if flags.NArg() == 1 {
		source = resolve(flags.Arg(0))
	}
	source, err := filepath.Abs(source)
	if err != nil {
		return err
	}
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source is not a regular file: %s", source)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	uri := (&url.URL{Scheme: "file", Path: source, RawQuery: "mode=ro"}).String()
	if *dryRun {
		db, err := sql.Open("sqlite3", uri+"&_query_only=on")
		if err != nil {
			return err
		}
		defer db.Close()
		for _, table := range importTables {
			columns, err := legacyColumns(ctx, db, "main", table)
			if err != nil {
				return err
			}
			var count int64
			if columns != "" {
				err = db.QueryRowContext(ctx, "SELECT count(*) FROM "+table.name).Scan(&count)
				if err != nil {
					return err
				}
			}
			fmt.Printf("would copy %d row(s) into %s\n", count, table.name)
		}
		return nil
	}
	if *target == "" {
		return fmt.Errorf("specify a new destination with --target or GEX_DATABASE_PATH")
	}
	destination, err := filepath.Abs(resolve(*target))
	if err != nil {
		return err
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0700); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(parent, ".tidedesk-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	stagedDB := filepath.Join(stage, "import.db")
	store, err := Open(*root, stagedDB)
	if err != nil {
		return err
	}
	defer store.Close()
	counts, err := copyLegacyData(ctx, store, uri)
	if err != nil {
		return err
	}
	// Consolidate the WAL into the file before making it visible to the app.
	var busy, logPages, checkpointed int
	if err := store.write.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)").Scan(&busy, &logPages, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return fmt.Errorf("could not checkpoint imported database")
	}
	store.Close()
	if err := ctx.Err(); err != nil {
		return err
	}
	// A hard link publishes the finished file without overwriting a target
	// created by another process while the import was running.
	if err := os.Link(stagedDB, destination); err != nil {
		return err
	}
	for i, table := range importTables {
		fmt.Printf("copied %d row(s) into %s\n", counts[i], table.name)
	}
	fmt.Println("Created", destination)
	return nil
}

type importQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func legacyColumns(ctx context.Context, conn importQuerier, schema string, table importTable) (string, error) {
	rows, err := conn.QueryContext(ctx, "PRAGMA "+schema+".table_info("+table.name+")")
	if err != nil {
		return "", err
	}
	defer rows.Close()
	present := make(map[string]bool)
	for rows.Next() {
		var id, notNull, key int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&id, &name, &typ, &notNull, &defaultValue, &key); err != nil {
			return "", err
		}
		present[name] = true
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if len(present) == 0 && table.name != "option_snapshots" {
		return "", nil
	}
	columns := strings.Fields(table.required)
	for _, column := range columns {
		if !present[column] {
			return "", fmt.Errorf("source %s lacks required column %s", table.name, column)
		}
	}
	for _, column := range strings.Fields(table.optional) {
		if present[column] {
			columns = append(columns, column)
		}
	}
	return strings.Join(columns, ","), nil
}

func copyLegacyData(ctx context.Context, store *Store, uri string) ([]int64, error) {
	conn, err := store.write.Conn(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS legacy", uri); err != nil {
		return nil, err
	}
	defer conn.ExecContext(context.Background(), "DETACH DATABASE legacy")
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	counts := make([]int64, len(importTables))
	for i, table := range importTables {
		columns, err := legacyColumns(ctx, tx, "legacy", table)
		if err != nil {
			return nil, err
		}
		if columns == "" {
			continue
		}
		statement := fmt.Sprintf("INSERT INTO main.%s(%s) SELECT %s FROM legacy.%s", table.name, columns, columns, table.name)
		result, err := tx.ExecContext(ctx, statement)
		if err != nil {
			return nil, fmt.Errorf("import %s: %w", table.name, err)
		}
		counts[i], err = result.RowsAffected()
		if err != nil {
			return nil, err
		}
	}
	// The initial migration ran on an empty store. Rebuild the same two
	// rollups for the imported rows, including historical archived snapshots.
	if _, err := tx.ExecContext(ctx, `
		WITH history AS (
			SELECT ticker,source,expiry,strike,option_type,collected_at FROM option_snapshots
			UNION ALL
			SELECT ticker,source,expiry,strike,option_type,collected_at FROM option_snapshots_archive
		)
		INSERT INTO contract_registry(ticker,source,expiry,strike,option_type,first_seen_at,last_seen_at,snapshots)
		SELECT ticker,source,expiry,strike,option_type,min(collected_at),max(collected_at),count(*)
		FROM history GROUP BY ticker,source,expiry,strike,option_type;

		WITH history AS (
			SELECT ticker,source,collected_at,expiry,implied_volatility,volume FROM option_snapshots
			UNION ALL
			SELECT ticker,source,collected_at,expiry,implied_volatility,volume FROM option_snapshots_archive
		), weighted AS (
			SELECT ticker,source,collected_at,implied_volatility,
				CASE WHEN implied_volatility IS NOT NULL AND expiry>=date(collected_at) THEN coalesce(volume,0) ELSE 0 END AS weight
			FROM history
		)
		INSERT INTO snapshot_iv_summary(ticker,source,collected_at,iv_weighted_avg)
		SELECT ticker,source,collected_at,
			CASE WHEN sum(weight)>0 THEN sum(implied_volatility*weight)/sum(weight) END
		FROM weighted GROUP BY ticker,source,collected_at;
	`); err != nil {
		return nil, err
	}
	return counts, tx.Commit()
}
