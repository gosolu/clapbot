package store

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// MemoryHit represents a search match from long-term memory or session history.
type MemoryHit struct {
	Source    string    `json:"source"`
	Topic     string    `json:"topic"`
	Snippet   string    `json:"snippet"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IndexMemoryEntry indexes or updates a memory topic/note in SQLite (and FTS5 when available).
func (s *SQLiteStore) IndexMemoryEntry(ctx context.Context, topic, content, source string) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM memory_entries WHERE topic = ? AND source = ?`, topic, source); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO memory_entries (topic, content, source, updated_at) VALUES (?, ?, ?, ?)`,
		topic, content, source, now,
	); err != nil {
		return err
	}

	if s.hasFTS5 {
		_, _ = tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE topic = ? AND source = ?`, topic, source)
		_, _ = tx.ExecContext(ctx, `INSERT INTO memory_fts (topic, content, source) VALUES (?, ?, ?)`, topic, content, source)
	}
	return tx.Commit()
}

// SearchMemory searches indexed memory notes and conversation history.
func (s *SQLiteStore) SearchMemory(ctx context.Context, query string, limit int) ([]MemoryHit, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, nil
	}
	if limit <= 0 {
		limit = 10
	}

	var hits []MemoryHit
	seen := map[string]bool{}

	addHit := func(h MemoryHit) {
		key := h.Source + ":" + h.Topic + ":" + h.Snippet
		if !seen[key] && len(hits) < limit {
			seen[key] = true
			hits = append(hits, h)
		}
	}

	if s.hasFTS5 {
		ftsQuery := sanitizeFTSQuery(query)
		if ftsQuery != "" {
			rows, err := s.db.QueryContext(ctx,
				`SELECT topic, content, source FROM memory_fts WHERE memory_fts MATCH ? LIMIT ?`,
				ftsQuery, limit,
			)
			if err == nil {
				for rows.Next() {
					var topic, content, source string
					if err := rows.Scan(&topic, &content, &source); err == nil {
						addHit(MemoryHit{
							Source:  source,
							Topic:   topic,
							Snippet: truncateSnippet(content, 400),
						})
					}
				}
				_ = rows.Close()
			}
		}
	}

	if len(hits) < limit {
		likePat := "%" + query + "%"
		rows, err := s.db.QueryContext(ctx,
			`SELECT topic, content, source, updated_at FROM memory_entries
			 WHERE topic LIKE ? OR content LIKE ?
			 ORDER BY updated_at DESC LIMIT ?`,
			likePat, likePat, limit,
		)
		if err == nil {
			for rows.Next() {
				var topic, content, source string
				var updatedAt time.Time
				if err := rows.Scan(&topic, &content, &source, &updatedAt); err == nil {
					addHit(MemoryHit{
						Source:    source,
						Topic:     topic,
						Snippet:   truncateSnippet(content, 400),
						UpdatedAt: updatedAt,
					})
				}
			}
			_ = rows.Close()
		}
	}

	// Also search past session messages if we still have room.
	if len(hits) < limit {
		likePat := "%" + query + "%"
		rows, err := s.db.QueryContext(ctx,
			`SELECT session_id, role, text_content, created_at FROM session_messages
			 WHERE text_content LIKE ?
			 ORDER BY created_at DESC LIMIT ?`,
			likePat, limit-len(hits),
		)
		if err == nil {
			for rows.Next() {
				var sid, role, txt string
				var createdAt time.Time
				if err := rows.Scan(&sid, &role, &txt, &createdAt); err == nil {
					addHit(MemoryHit{
						Source:    fmt.Sprintf("session:%s", sid),
						Topic:     role,
						Snippet:   truncateSnippet(txt, 300),
						UpdatedAt: createdAt,
					})
				}
			}
			_ = rows.Close()
		}
	}

	return hits, nil
}

func sanitizeFTSQuery(q string) string {
	fields := strings.FieldsFunc(q, func(r rune) bool {
		return (r < 'a' || r > 'z') &&
			(r < 'A' || r > 'Z') &&
			(r < '0' || r > '9') &&
			r <= 127 &&
			r != '_' &&
			r != '-'
	})
	if len(fields) == 0 {
		return ""
	}
	var quoted []string
	for _, f := range fields {
		quoted = append(quoted, `"`+f+`"`)
	}
	return strings.Join(quoted, " OR ")
}

func truncateSnippet(s string, maxChars int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxChars {
		return s
	}
	return string(runes[:maxChars]) + "..."
}
