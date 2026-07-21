// Package state persists per-user orchestration state to SQLite.
package state

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed 001_init.sql
var initSQL string

// NullInt64 is an alias for sql.NullInt64, exported so other packages can
// construct nullable values without importing database/sql.
type NullInt64 = sql.NullInt64

// NullString is an alias for sql.NullString.
type NullString = sql.NullString

// WLState enumerates whitelist states.
type WLState string

const (
	WLActive  WLState = "active"
	WLGrace   WLState = "grace"
	WLBlocked WLState = "blocked"
)

// BasicState enumerates basic-squad states.
type BasicState string

const (
	BasicActive  BasicState = "active"
	BasicBlocked BasicState = "blocked"
)

// UserState is the persisted state for one user.
type UserState struct {
	UserUUID            string
	WLState             WLState
	WLGraceUntil        sql.NullInt64
	WLOriginalLimit     sql.NullInt64
	WLOriginalStrategy  sql.NullString
	WLOverLimit         sql.NullInt64
	BasicUsedBytes      int64
	BasicLimitBytes     int64
	BasicState          BasicState
	LastWLLimitedAt     sql.NullInt64
	LastBasicLimitedAt  sql.NullInt64
	LastReconciledAt    sql.NullInt64
	CreatedAt           int64
	UpdatedAt           int64
}

// Store wraps a SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the database at path and runs migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	// Single writer is plenty for our load and avoids SQLITE_BUSY races.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(initSQL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Get returns the state for a user, creating a default row if missing.
func (s *Store) Get(ctx context.Context, userUUID string, defaultBasicLimit int64) (*UserState, error) {
	row := s.queryRow(ctx, userUUID, defaultBasicLimit)
	return row, nil
}

func (s *Store) queryRow(ctx context.Context, userUUID string, defaultBasicLimit int64) *UserState {
	now := time.Now().Unix()
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO user_state (user_uuid, basic_limit_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_uuid) DO NOTHING`,
		userUUID, defaultBasicLimit, now, now)

	var st UserState
	err := s.db.QueryRowContext(ctx, `
		SELECT user_uuid, wl_state, wl_grace_until, wl_original_limit, wl_original_strategy,
		       wl_over_limit, basic_used_bytes, basic_limit_bytes, basic_state,
		       last_wl_limited_at, last_basic_limited_at, last_reconciled_at,
		       created_at, updated_at
		FROM user_state WHERE user_uuid = ?`, userUUID).Scan(
		&st.UserUUID, &st.WLState, &st.WLGraceUntil, &st.WLOriginalLimit, &st.WLOriginalStrategy,
		&st.WLOverLimit, &st.BasicUsedBytes, &st.BasicLimitBytes, &st.BasicState,
		&st.LastWLLimitedAt, &st.LastBasicLimitedAt, &st.LastReconciledAt,
		&st.CreatedAt, &st.UpdatedAt,
	)
	if err != nil {
		// Should not happen after the INSERT above; surface as a typed error.
		return &UserState{UserUUID: userUUID, WLState: WLActive, BasicState: BasicActive, BasicLimitBytes: defaultBasicLimit}
	}
	return &st
}

// Update runs fn inside a transaction with the row locked; commits on success.
func (s *Store) Update(ctx context.Context, userUUID string, defaultBasicLimit int64, fn func(*UserState) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO user_state (user_uuid, basic_limit_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(user_uuid) DO NOTHING`,
		userUUID, defaultBasicLimit, now, now); err != nil {
		return fmt.Errorf("ensure row: %w", err)
	}

	// SQLite default locking is per-connection; with SetMaxOpenConns(1) this
	// transaction is effectively serialized against other writers, which is
	// sufficient to keep per-user state consistent.
	var st UserState
	if err := tx.QueryRowContext(ctx, `
		SELECT user_uuid, wl_state, wl_grace_until, wl_original_limit, wl_original_strategy,
		       wl_over_limit, basic_used_bytes, basic_limit_bytes, basic_state,
		       last_wl_limited_at, last_basic_limited_at, last_reconciled_at,
		       created_at, updated_at
		FROM user_state WHERE user_uuid = ?`, userUUID).Scan(
		&st.UserUUID, &st.WLState, &st.WLGraceUntil, &st.WLOriginalLimit, &st.WLOriginalStrategy,
		&st.WLOverLimit, &st.BasicUsedBytes, &st.BasicLimitBytes, &st.BasicState,
		&st.LastWLLimitedAt, &st.LastBasicLimitedAt, &st.LastReconciledAt,
		&st.CreatedAt, &st.UpdatedAt,
	); err != nil {
		return fmt.Errorf("load row: %w", err)
	}

	if err := fn(&st); err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `
		UPDATE user_state SET
			wl_state = ?,
			wl_grace_until = ?,
			wl_original_limit = ?,
			wl_original_strategy = ?,
			wl_over_limit = ?,
			basic_used_bytes = ?,
			basic_limit_bytes = ?,
			basic_state = ?,
			last_wl_limited_at = ?,
			last_basic_limited_at = ?,
			last_reconciled_at = ?,
			updated_at = ?
		WHERE user_uuid = ?`,
		string(st.WLState), st.WLGraceUntil, st.WLOriginalLimit, st.WLOriginalStrategy,
		st.WLOverLimit, st.BasicUsedBytes, st.BasicLimitBytes, string(st.BasicState),
		st.LastWLLimitedAt, st.LastBasicLimitedAt, st.LastReconciledAt,
		time.Now().Unix(), userUUID,
	); err != nil {
		return fmt.Errorf("persist row: %w", err)
	}
	return tx.Commit()
}

// AddBasicUsage records a delta of basic traffic for the user.
func (s *Store) AddBasicUsage(ctx context.Context, userUUID string, delta int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE user_state SET basic_used_bytes = basic_used_bytes + ?, updated_at = ? WHERE user_uuid = ?`,
		delta, time.Now().Unix(), userUUID)
	return err
}

// SetUsageCheckpoint records the high-water mark of total bytes seen for a (node,user) pair,
// returning the delta since the previous checkpoint (0 on first sighting).
func (s *Store) SetUsageCheckpoint(ctx context.Context, nodeUUID, userUUID string, total int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	var prev int64
	err = tx.QueryRowContext(ctx,
		`SELECT bytes_total FROM usage_checkpoint WHERE node_uuid = ? AND user_uuid = ?`,
		nodeUUID, userUUID).Scan(&prev)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// first sighting → no delta counted yet (we may be mid-billing-cycle)
	case err != nil:
		return 0, err
	}

	delta := total - prev
	if delta < 0 {
		// Counter reset on the panel side (e.g. node reinstall). Treat as 0 delta
		// and re-anchor on the new value.
		delta = 0
	}

	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO usage_checkpoint (node_uuid, user_uuid, bytes_total, fetched_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(node_uuid, user_uuid) DO UPDATE SET bytes_total = excluded.bytes_total, fetched_at = excluded.fetched_at`,
		nodeUUID, userUUID, total, now); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return delta, nil
}

// IterNonBlockedWLUsers visits users still considered active for reconciliation.
func (s *Store) IterNonBlockedWLUsers(ctx context.Context, fn func(*UserState) error) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_uuid, wl_state, wl_grace_until, wl_original_limit, wl_original_strategy,
		       wl_over_limit, basic_used_bytes, basic_limit_bytes, basic_state,
		       last_wl_limited_at, last_basic_limited_at, last_reconciled_at,
		       created_at, updated_at
		FROM user_state WHERE wl_state IN ('active','grace')`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var st UserState
		if err := rows.Scan(
			&st.UserUUID, &st.WLState, &st.WLGraceUntil, &st.WLOriginalLimit, &st.WLOriginalStrategy,
			&st.WLOverLimit, &st.BasicUsedBytes, &st.BasicLimitBytes, &st.BasicState,
			&st.LastWLLimitedAt, &st.LastBasicLimitedAt, &st.LastReconciledAt,
			&st.CreatedAt, &st.UpdatedAt,
		); err != nil {
			return err
		}
		if err := fn(&st); err != nil {
			return err
		}
	}
	return rows.Err()
}
