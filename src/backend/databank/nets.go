package databank

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// BoardNets is the net-name list for one board file, submitted once by the
// frontend after it parses the boardview locally. The Go backend never parses
// board formats itself (see docs/assistant/ARCHITECTURE.md) — this table only
// stores what the browser already computed. Names are kept exactly as
// received; no case/separator normalization happens here.
type BoardNets struct {
	FileID     int64    `json:"file_id"`
	Nets       []string `json:"nets"`
	ReceivedAt int64    `json:"received_at"`
}

// ErrBoardNetsNotFound is returned by GetBoardNets when the file exists but no
// net list has been submitted for it yet. Callers must not treat this as a
// server error — it's an expected, common state for a board that hasn't been
// opened by a live client yet.
var ErrBoardNetsNotFound = errors.New("board nets not found")

// UpsertBoardNets stores the net-name list for fileID, replacing any prior
// list in place. Idempotent: resubmitting the same (or a different) list for
// the same fileID overwrites the single existing row — it never inserts a
// duplicate and the table never grows past one row per file.
func (db *DB) UpsertBoardNets(fileID int64, nets []string, receivedAt int64) error {
	if nets == nil {
		nets = []string{}
	}
	payload, err := json.Marshal(nets)
	if err != nil {
		return err
	}

	db.mu.Lock()
	defer db.mu.Unlock()

	_, err = db.writer.Exec(`
		INSERT INTO board_nets (file_id, nets_json, net_count, received_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(file_id) DO UPDATE SET
			nets_json = excluded.nets_json,
			net_count = excluded.net_count,
			received_at = excluded.received_at
	`, fileID, string(payload), len(nets), receivedAt)
	return err
}

// GetBoardNets returns the stored net list for fileID, or
// ErrBoardNetsNotFound if none has been submitted yet.
func (db *DB) GetBoardNets(ctx context.Context, fileID int64) (*BoardNets, error) {
	var payload string
	var receivedAt int64
	row := db.reader.QueryRowContext(ctx,
		`SELECT nets_json, received_at FROM board_nets WHERE file_id = ?`, fileID)
	if err := row.Scan(&payload, &receivedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrBoardNetsNotFound
		}
		return nil, err
	}
	var nets []string
	if err := json.Unmarshal([]byte(payload), &nets); err != nil {
		return nil, err
	}
	return &BoardNets{FileID: fileID, Nets: nets, ReceivedAt: receivedAt}, nil
}
