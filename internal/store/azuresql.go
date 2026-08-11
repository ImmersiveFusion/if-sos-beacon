package store

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	// The azuread driver registers the "azuresql" driver name and stays pure Go
	// (no cgo), preserving CGO_ENABLED=0 / distroless parity. One driver covers
	// both auth modes with no code fork: a DSN with fedauth=ActiveDirectoryDefault
	// uses DefaultAzureCredential (local `az login`, or workload identity in a
	// cluster); a plain sqlserver://user:pass@host?database=... DSN uses SQL auth.
	_ "github.com/microsoft/go-mssqldb/azuread"

	"github.com/ImmersiveFusion/sos-beacon/internal/config"
	"github.com/ImmersiveFusion/sos-beacon/internal/core"
)

// storeDSNEnv overrides the configured DSN (the cluster secret-store path, where
// an External Secrets Operator / Key Vault projects the connection string in).
const storeDSNEnv = "SOS_STORE_DSN"

// opTimeout bounds each database round-trip. The Store interface is context-free
// (Phase 0), so the adapter supplies its own bounded context per call.
const opTimeout = 15 * time.Second

//go:embed schema.sql
var schemaSQL string

// SQL statements, shared with the tests so they assert the real queries. All
// use parameterized placeholders; fetched content never enters a statement as
// text.
const (
	sqlSeenSelect    = `SELECT 1 FROM seen WHERE beacon = @p1 AND source = @p2 AND id = @p3`
	sqlSeenInsert    = `IF NOT EXISTS (SELECT 1 FROM seen WHERE beacon = @p1 AND source = @p2 AND id = @p3) INSERT INTO seen (beacon, source, id) VALUES (@p1, @p2, @p3)`
	sqlFindingUpsert = `UPDATE finding SET data = @p4 WHERE beacon = @p1 AND source = @p2 AND id = @p3; IF @@ROWCOUNT = 0 INSERT INTO finding (beacon, source, id, data) VALUES (@p1, @p2, @p3, @p4)`
	sqlPendingSelect = `SELECT data FROM finding WHERE beacon = @p1`
	sqlClaimUpsert   = `UPDATE claim SET claimant = @p2, ts = SYSUTCDATETIME() WHERE finding_id = @p1; IF @@ROWCOUNT = 0 INSERT INTO claim (finding_id, claimant) VALUES (@p1, @p2)`
	sqlPurgeSeen     = `DELETE FROM seen WHERE source = @p1`
	sqlPurgeFinding  = `DELETE FROM finding WHERE source = @p1`
)

// AzureSQL is a Store backed by Azure SQL / SQL Server via the pure-Go
// go-mssqldb azuread driver. It implements every persistence role (Deduper,
// Recorder, Ledger, io.Closer).
type AzureSQL struct {
	db *sql.DB
}

var _ core.Store = (*AzureSQL)(nil)

// OpenAzureSQL resolves a DSN (env override, explicit dsn, or composed from
// server+database), opens the connection, verifies it, and ensures the schema.
func OpenAzureSQL(cfg config.StoreConfig) (*AzureSQL, error) {
	dsn, err := resolveDSN(cfg)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("azuresql", dsn)
	if err != nil {
		return nil, fmt.Errorf("azuresql: open: %w", err)
	}
	s := &AzureSQL{db: db}

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("azuresql: connect: %w", err)
	}
	if err := s.ensureSchema(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// resolveDSN picks the connection string. Precedence: SOS_STORE_DSN env, then an
// explicit cfg.DSN, then a composed fedauth DSN from server+database. No server
// name or secret is ever baked into the binary or a committed config (D12).
func resolveDSN(cfg config.StoreConfig) (string, error) {
	if env := os.Getenv(storeDSNEnv); env != "" {
		return env, nil
	}
	if cfg.DSN != "" {
		return cfg.DSN, nil
	}
	if cfg.Server != "" && cfg.Database != "" {
		return fmt.Sprintf("server=%s;database=%s;fedauth=ActiveDirectoryDefault", cfg.Server, cfg.Database), nil
	}
	return "", errors.New("azuresql: need a dsn, or server+database, or the SOS_STORE_DSN env var")
}

func (s *AzureSQL) ensureSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("azuresql: ensure schema: %w", err)
	}
	return nil
}

func (s *AzureSQL) opCtx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), opTimeout)
}

// Seen reports whether the beacon has already processed (source, id).
func (s *AzureSQL) Seen(beacon, source, id string) (bool, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	var one int
	err := s.db.QueryRowContext(ctx, sqlSeenSelect, beacon, source, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("azuresql: seen: %w", err)
	}
	return true, nil
}

// MarkSeen records (source, id) as processed for the beacon, without delivering.
func (s *AzureSQL) MarkSeen(beacon, source, id string) error {
	ctx, cancel := s.opCtx()
	defer cancel()
	if _, err := s.db.ExecContext(ctx, sqlSeenInsert, beacon, source, id); err != nil {
		return fmt.Errorf("azuresql: mark seen: %w", err)
	}
	return nil
}

// Record persists a delivered/digest finding and marks its signal seen, in one
// transaction.
func (s *AzureSQL) Record(f core.Finding) error {
	data, err := json.Marshal(f)
	if err != nil {
		return fmt.Errorf("azuresql: marshal finding: %w", err)
	}

	ctx, cancel := s.opCtx()
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("azuresql: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, sqlFindingUpsert, f.Beacon, f.Signal.Source, f.Signal.ID, string(data)); err != nil {
		return fmt.Errorf("azuresql: upsert finding: %w", err)
	}
	if _, err := tx.ExecContext(ctx, sqlSeenInsert, f.Beacon, f.Signal.Source, f.Signal.ID); err != nil {
		return fmt.Errorf("azuresql: mark seen: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("azuresql: commit: %w", err)
	}
	return nil
}

// Claim records that claimant has taken responsibility for a finding.
func (s *AzureSQL) Claim(findingID, claimant string) error {
	ctx, cancel := s.opCtx()
	defer cancel()
	if _, err := s.db.ExecContext(ctx, sqlClaimUpsert, findingID, claimant); err != nil {
		return fmt.Errorf("azuresql: claim: %w", err)
	}
	return nil
}

// PendingDigest returns the findings recorded for one beacon.
func (s *AzureSQL) PendingDigest(beacon string) ([]core.Finding, error) {
	ctx, cancel := s.opCtx()
	defer cancel()
	rows, err := s.db.QueryContext(ctx, sqlPendingSelect, beacon)
	if err != nil {
		return nil, fmt.Errorf("azuresql: pending digest: %w", err)
	}
	defer rows.Close()

	var out []core.Finding
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, fmt.Errorf("azuresql: scan finding: %w", err)
		}
		var f core.Finding
		if err := json.Unmarshal([]byte(data), &f); err != nil {
			return nil, fmt.Errorf("azuresql: unmarshal finding: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("azuresql: iterate findings: %w", err)
	}
	return out, nil
}

// PurgeSource deletes every dedupe row and recorded finding that came from one
// source, across all beacons, and reports how many rows went (core.Purger).
//
// Both deletes run in one transaction: a purge that dropped the findings but
// left the dedupe rows (or the reverse) is a worse state than either doing it or
// not, and the reason this path exists at all is a revocation clock.
//
// The claim table is keyed by an opaque finding id, so it is not source-mappable
// today. Nothing writes it yet, and it holds only a claimant's own name, never
// fetched content. The claim bot must key by (source, id) so this can clear it.
func (s *AzureSQL) PurgeSource(source string) (int, error) {
	ctx, cancel := s.opCtx()
	defer cancel()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("azuresql: purge begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	total := 0
	for _, stmt := range []string{sqlPurgeFinding, sqlPurgeSeen} {
		res, execErr := tx.ExecContext(ctx, stmt, source)
		if execErr != nil {
			return 0, fmt.Errorf("azuresql: purge %q: %w", source, execErr)
		}
		if n, rowsErr := res.RowsAffected(); rowsErr == nil {
			total += int(n)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("azuresql: purge commit: %w", err)
	}
	return total, nil
}

// Close releases the underlying connection pool.
func (s *AzureSQL) Close() error { return s.db.Close() }
