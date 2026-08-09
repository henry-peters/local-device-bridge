package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/local-device-bridge/local-device-bridge/internal/core"
	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open state database: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
CREATE TABLE IF NOT EXISTS devices (
 id TEXT PRIMARY KEY,
 metadata TEXT NOT NULL,
 updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 device_id TEXT NOT NULL,
 action TEXT NOT NULL,
 principal TEXT NOT NULL,
 source TEXT NOT NULL,
 success INTEGER NOT NULL,
 message TEXT NOT NULL,
 created_at TEXT NOT NULL
);`)
	if err != nil {
		return fmt.Errorf("migrate state database: %w", err)
	}
	return nil
}

func (s *Store) LoadDevices(ctx context.Context) ([]core.DeviceMetadata, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT metadata FROM devices ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []core.DeviceMetadata
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var md core.DeviceMetadata
		if err := json.Unmarshal([]byte(raw), &md); err != nil {
			return nil, fmt.Errorf("decode device metadata: %w", err)
		}
		result = append(result, md)
	}
	return result, rows.Err()
}

func (s *Store) SaveDevice(ctx context.Context, md core.DeviceMetadata) error {
	raw, err := json.Marshal(md)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO devices(id, metadata, updated_at) VALUES(?,?,?) ON CONFLICT(id) DO UPDATE SET metadata=excluded.metadata, updated_at=excluded.updated_at`, string(md.ID), string(raw), time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) Audit(ctx context.Context, cmd core.Command, success bool, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(device_id, action, principal, source, success, message, created_at) VALUES(?,?,?,?,?,?,?)`, string(cmd.DeviceID), string(cmd.Action), cmd.Principal, cmd.Source, success, message, time.Now().UTC().Format(time.RFC3339Nano))
	return err
}

func (s *Store) ListAudit(ctx context.Context, limit int) ([]core.AuditEvent, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, device_id, action, principal, source, success, message, created_at FROM audit_events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []core.AuditEvent
	for rows.Next() {
		var event core.AuditEvent
		var success int
		if err := rows.Scan(&event.ID, &event.DeviceID, &event.Action, &event.Principal, &event.Source, &success, &event.Message, &event.CreatedAt); err != nil {
			return nil, err
		}
		event.Success = success != 0
		result = append(result, event)
	}
	return result, rows.Err()
}

func (s *Store) Close() error { return s.db.Close() }
