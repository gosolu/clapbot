package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gosolu/react"
	_ "modernc.org/sqlite"
)

// Session represents metadata for a conversation session.
type Session struct {
	ID           string    `json:"id"`
	Title        string    `json:"title"`
	WorkspaceDir string    `json:"workspace_dir"`
	Profile      string    `json:"profile"`
	MessageCount int       `json:"message_count"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

// Schedule represents a recurring cron job or one-shot timer persisted in SQLite.
type Schedule struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	CronExpr  string     `json:"cron_expr,omitempty"` // Standard 5-field cron expression
	RunAt     *time.Time `json:"run_at,omitempty"`    // Non-nil for one-shot timers
	Prompt    string     `json:"prompt"`
	Channel   string     `json:"channel"`    // cli | telegram | feishu
	ChatID    string     `json:"chat_id"`    // Target chat ID for delivery
	SessionID string     `json:"session_id"` // Session to run in (empty = ephemeral or bound)
	Enabled   bool       `json:"enabled"`
	CreatedAt time.Time  `json:"created_at"`
	LastRunAt *time.Time `json:"last_run_at,omitempty"`
}

// SQLiteStore implements react.Store and manages sessions, channel bindings, schedules, and search.
type SQLiteStore struct {
	db      *sql.DB
	writeMu sync.Mutex
	hasFTS5 bool
}

// Open opens or creates the SQLite store at dbPath (or ":memory:" for tests).
func Open(dbPath string) (*SQLiteStore, error) {
	dsn := dbPath
	if dbPath != ":memory:" && !strings.Contains(dbPath, "?") {
		dsn = dbPath + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite %s: %w", dbPath, err)
	}
	if dbPath == ":memory:" {
		db.SetMaxOpenConns(1)
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the underlying database connection.
func (s *SQLiteStore) Close() error {
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			title TEXT NOT NULL DEFAULT '',
			workspace_dir TEXT NOT NULL DEFAULT '',
			profile TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			session_id TEXT NOT NULL,
			seq INTEGER NOT NULL,
			role TEXT NOT NULL,
			text_content TEXT NOT NULL DEFAULT '',
			payload_json TEXT NOT NULL,
			created_at DATETIME NOT NULL
		);`,
		`CREATE INDEX IF NOT EXISTS idx_session_messages_sid_seq ON session_messages(session_id, seq);`,
		`CREATE TABLE IF NOT EXISTS channel_bindings (
			channel TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			session_id TEXT NOT NULL,
			updated_at DATETIME NOT NULL,
			PRIMARY KEY (channel, chat_id)
		);`,
		`CREATE TABLE IF NOT EXISTS schedules (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			cron_expr TEXT NOT NULL DEFAULT '',
			run_at DATETIME,
			prompt TEXT NOT NULL,
			channel TEXT NOT NULL DEFAULT '',
			chat_id TEXT NOT NULL DEFAULT '',
			session_id TEXT NOT NULL DEFAULT '',
			enabled INTEGER NOT NULL DEFAULT 1,
			created_at DATETIME NOT NULL,
			last_run_at DATETIME
		);`,
		`CREATE TABLE IF NOT EXISTS memory_entries (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			topic TEXT NOT NULL COLLATE NOCASE,
			content TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'MEMORY.md',
			updated_at DATETIME NOT NULL
		);`,
	}

	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("store: migrate: %w", err)
		}
	}

	// Attempt FTS5 virtual table creation; gracefully fall back to LIKE search if unavailable.
	if _, err := s.db.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(topic, content, source);`); err == nil {
		s.hasFTS5 = true
	}
	return nil
}

// Load implements react.Store.
func (s *SQLiteStore) Load(ctx context.Context, sessionID string) ([]react.Message, error) {
	if sessionID == "" {
		return nil, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT payload_json FROM session_messages WHERE session_id = ? ORDER BY seq ASC, id ASC`,
		sessionID,
	)
	if err != nil {
		return nil, fmt.Errorf("store: load session %s: %w", sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	var msgs []react.Message
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var m react.Message
		if err := json.Unmarshal([]byte(raw), &m); err != nil {
			return nil, fmt.Errorf("store: unmarshal message in session %s: %w", sessionID, err)
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return msgs, nil
}

// Append implements react.Store.
func (s *SQLiteStore) Append(ctx context.Context, sessionID string, msgs ...react.Message) error {
	if sessionID == "" || len(msgs) == 0 {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// Ensure session row exists.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO sessions (id, title, workspace_dir, profile, created_at, updated_at)
		 VALUES (?, '', '', '', ?, ?)
		 ON CONFLICT(id) DO UPDATE SET updated_at = excluded.updated_at`,
		sessionID, now, now,
	)
	if err != nil {
		return err
	}

	var maxSeq sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(seq) FROM session_messages WHERE session_id = ?`, sessionID).Scan(&maxSeq); err != nil {
		return err
	}
	seq := int(maxSeq.Int64)

	var firstUserText string
	for _, m := range msgs {
		seq++
		cloned := m.Clone()
		b, err := json.Marshal(cloned)
		if err != nil {
			return err
		}
		txt := cloned.Text()
		if firstUserText == "" && cloned.Role == react.RoleUser && strings.TrimSpace(txt) != "" && !strings.HasPrefix(strings.TrimSpace(txt), "<system-reminder>") {
			firstUserText = strings.TrimSpace(txt)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_messages (session_id, seq, role, text_content, payload_json, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, seq, string(cloned.Role), txt, string(b), now,
		); err != nil {
			return err
		}
	}

	if firstUserText != "" {
		title := firstUserText
		if idx := strings.IndexAny(title, "\r\n"); idx >= 0 {
			title = title[:idx]
		}
		runes := []rune(title)
		if len(runes) > 60 {
			title = string(runes[:60]) + "..."
		}
		_, _ = tx.ExecContext(ctx,
			`UPDATE sessions SET title = ? WHERE id = ? AND title = ''`,
			title, sessionID,
		)
	}

	return tx.Commit()
}

// Replace implements react.Store.
func (s *SQLiteStore) Replace(ctx context.Context, sessionID string, msgs []react.Message) error {
	if sessionID == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	_, err = tx.ExecContext(ctx,
		`INSERT INTO sessions (id, title, workspace_dir, profile, created_at, updated_at)
		 VALUES (?, '', '', '', ?, ?)
		 ON CONFLICT(id) DO UPDATE SET updated_at = excluded.updated_at`,
		sessionID, now, now,
	)
	if err != nil {
		return err
	}

	if _, err := tx.ExecContext(ctx, `DELETE FROM session_messages WHERE session_id = ?`, sessionID); err != nil {
		return err
	}

	for i, m := range msgs {
		cloned := m.Clone()
		b, err := json.Marshal(cloned)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO session_messages (session_id, seq, role, text_content, payload_json, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			sessionID, i+1, string(cloned.Role), cloned.Text(), string(b), now,
		); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// Delete implements react.Store.
func (s *SQLiteStore) Delete(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM session_messages WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearMessages deletes all messages in a session while keeping its metadata (workspace, profile).
func (s *SQLiteStore) ClearMessages(ctx context.Context, sessionID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM session_messages WHERE session_id = ?`, sessionID)
	return err
}

// EnsureSession creates a session if it doesn't exist, or returns the existing one (filling empty defaults).
func (s *SQLiteStore) EnsureSession(ctx context.Context, sessionID, defaultWorkspace, defaultProfile string) (*Session, error) {
	s.writeMu.Lock()
	now := time.Now().UTC()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, title, workspace_dir, profile, created_at, updated_at)
		 VALUES (?, '', ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   workspace_dir = CASE WHEN sessions.workspace_dir = '' THEN excluded.workspace_dir ELSE sessions.workspace_dir END,
		   profile = CASE WHEN sessions.profile = '' THEN excluded.profile ELSE sessions.profile END`,
		sessionID, defaultWorkspace, defaultProfile, now, now,
	)
	s.writeMu.Unlock()
	if err != nil {
		return nil, err
	}
	return s.GetSession(ctx, sessionID)
}

// GetSession retrieves session metadata by ID.
func (s *SQLiteStore) GetSession(ctx context.Context, sessionID string) (*Session, error) {
	var sess Session
	err := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.title, s.workspace_dir, s.profile, s.created_at, s.updated_at,
		        (SELECT COUNT(*) FROM session_messages m WHERE m.session_id = s.id)
		 FROM sessions s WHERE s.id = ?`,
		sessionID,
	).Scan(&sess.ID, &sess.Title, &sess.WorkspaceDir, &sess.Profile, &sess.CreatedAt, &sess.UpdatedAt, &sess.MessageCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &sess, nil
}

// UpdateSessionWorkspace updates the bound workspace directory for a session.
func (s *SQLiteStore) UpdateSessionWorkspace(ctx context.Context, sessionID, workspaceDir string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET workspace_dir = ?, updated_at = ? WHERE id = ?`,
		workspaceDir, time.Now().UTC(), sessionID,
	)
	return err
}

// UpdateSessionProfile updates the bound model profile for a session.
func (s *SQLiteStore) UpdateSessionProfile(ctx context.Context, sessionID, profile string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`UPDATE sessions SET profile = ?, updated_at = ? WHERE id = ?`,
		profile, time.Now().UTC(), sessionID,
	)
	return err
}

// ListSessions lists recent sessions ordered by updated_at DESC.
func (s *SQLiteStore) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.title, s.workspace_dir, s.profile, s.created_at, s.updated_at,
		        (SELECT COUNT(*) FROM session_messages m WHERE m.session_id = s.id)
		 FROM sessions s
		 ORDER BY s.updated_at DESC
		 LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.Title, &sess.WorkspaceDir, &sess.Profile, &sess.CreatedAt, &sess.UpdatedAt, &sess.MessageCount); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// GetBoundSession returns the active sessionID for a (channel, chatID) pair, or "" if unbound.
func (s *SQLiteStore) GetBoundSession(ctx context.Context, channel, chatID string) (string, error) {
	var sid string
	err := s.db.QueryRowContext(ctx,
		`SELECT session_id FROM channel_bindings WHERE channel = ? AND chat_id = ?`,
		channel, chatID,
	).Scan(&sid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return sid, err
}

// SetBoundSession binds a (channel, chatID) pair to a sessionID.
func (s *SQLiteStore) SetBoundSession(ctx context.Context, channel, chatID, sessionID string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO channel_bindings (channel, chat_id, session_id, updated_at)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(channel, chat_id) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`,
		channel, chatID, sessionID, time.Now().UTC(),
	)
	return err
}

// SaveSchedule inserts or updates a schedule.
func (s *SQLiteStore) SaveSchedule(ctx context.Context, sch Schedule) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if sch.CreatedAt.IsZero() {
		sch.CreatedAt = time.Now().UTC()
	}
	enabled := 0
	if sch.Enabled {
		enabled = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO schedules (id, name, cron_expr, run_at, prompt, channel, chat_id, session_id, enabled, created_at, last_run_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name,
		   cron_expr = excluded.cron_expr,
		   run_at = excluded.run_at,
		   prompt = excluded.prompt,
		   channel = excluded.channel,
		   chat_id = excluded.chat_id,
		   session_id = excluded.session_id,
		   enabled = excluded.enabled,
		   last_run_at = excluded.last_run_at`,
		sch.ID, sch.Name, sch.CronExpr, sch.RunAt, sch.Prompt, sch.Channel, sch.ChatID, sch.SessionID, enabled, sch.CreatedAt, sch.LastRunAt,
	)
	return err
}

// ListSchedules returns all schedules.
func (s *SQLiteStore) ListSchedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, cron_expr, run_at, prompt, channel, chat_id, session_id, enabled, created_at, last_run_at
		 FROM schedules ORDER BY created_at ASC`,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Schedule
	for rows.Next() {
		var sch Schedule
		var enabled int
		var runAt, lastRunAt sql.NullTime
		if err := rows.Scan(&sch.ID, &sch.Name, &sch.CronExpr, &runAt, &sch.Prompt, &sch.Channel, &sch.ChatID, &sch.SessionID, &enabled, &sch.CreatedAt, &lastRunAt); err != nil {
			return nil, err
		}
		sch.Enabled = enabled != 0
		if runAt.Valid {
			t := runAt.Time
			sch.RunAt = &t
		}
		if lastRunAt.Valid {
			t := lastRunAt.Time
			sch.LastRunAt = &t
		}
		out = append(out, sch)
	}
	return out, rows.Err()
}

// DeleteSchedule removes a schedule by ID.
func (s *SQLiteStore) DeleteSchedule(ctx context.Context, id string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err := s.db.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id)
	return err
}

// MarkScheduleRun updates last_run_at and optionally disables one-shot schedules.
func (s *SQLiteStore) MarkScheduleRun(ctx context.Context, id string, disable bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	now := time.Now().UTC()
	if disable {
		_, err := s.db.ExecContext(ctx, `UPDATE schedules SET last_run_at = ?, enabled = 0 WHERE id = ?`, now, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE schedules SET last_run_at = ? WHERE id = ?`, now, id)
	return err
}
