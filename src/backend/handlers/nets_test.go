package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"boardripper/databank"
)

// netsTestHandler opens a temp databank, inserts one board file, and returns
// the handler + the file id, mirroring donorTestHandler's setup style.
func netsTestHandler(t *testing.T) (*DatabankHandler, *databank.DB, int64) {
	t.Helper()
	db, err := databank.Open(t.TempDir())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	id, err := db.InsertFile(&databank.FileRecord{
		Path: "051-7845/boardview/K22.brd", Filename: "K22.brd", Extension: ".brd",
		FileType: "board", Manufacturer: "051-7845",
	})
	if err != nil {
		t.Fatalf("InsertFile: %v", err)
	}
	return NewDatabankHandler(db, nil, t.TempDir()), db, id
}

func postNets(h *DatabankHandler, id int64, body string) *httptest.ResponseRecorder {
	idStr := strconv.FormatInt(id, 10)
	req := httptest.NewRequest("POST", "/api/databank/files/"+idStr+"/nets", bytes.NewBufferString(body))
	req.SetPathValue("id", idStr)
	w := httptest.NewRecorder()
	h.SetBoardNets(w, req)
	return w
}

func getNets(h *DatabankHandler, id int64) *httptest.ResponseRecorder {
	idStr := strconv.FormatInt(id, 10)
	req := httptest.NewRequest("GET", "/api/databank/files/"+idStr+"/nets", nil)
	req.SetPathValue("id", idStr)
	w := httptest.NewRecorder()
	h.GetBoardNets(w, req)
	return w
}

// TestGetBoardNets_NotYetSubmitted verifies the "не заполнено" state: the
// file exists but no list has been sent yet. That's 204, not an error.
func TestGetBoardNets_NotYetSubmitted(t *testing.T) {
	h, _, id := netsTestHandler(t)
	w := getNets(h, id)
	if w.Code != http.StatusNoContent {
		t.Fatalf("GET before POST: code = %d, want %d", w.Code, http.StatusNoContent)
	}
}

// TestBoardNets_RoundTrip verifies POST then GET returns exactly what was
// sent, plus the file's board key/format and a received_at timestamp.
func TestBoardNets_RoundTrip(t *testing.T) {
	h, _, id := netsTestHandler(t)

	w := postNets(h, id, `{"nets":["PP3V3_S0","GND","PP1V8_SUS"]}`)
	if w.Code != http.StatusOK {
		t.Fatalf("POST: code = %d body=%s", w.Code, w.Body.String())
	}
	var postResp struct {
		FileID     int64 `json:"file_id"`
		NetCount   int   `json:"net_count"`
		ReceivedAt int64 `json:"received_at"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &postResp); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	if postResp.FileID != id || postResp.NetCount != 3 || postResp.ReceivedAt == 0 {
		t.Fatalf("POST response = %+v", postResp)
	}

	g := getNets(h, id)
	if g.Code != http.StatusOK {
		t.Fatalf("GET: code = %d body=%s", g.Code, g.Body.String())
	}
	var getResp struct {
		FileID     int64    `json:"file_id"`
		BoardKey   string   `json:"board_key"`
		Format     string   `json:"format"`
		Nets       []string `json:"nets"`
		ReceivedAt int64    `json:"received_at"`
	}
	if err := json.Unmarshal(g.Body.Bytes(), &getResp); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if getResp.FileID != id {
		t.Errorf("file_id = %d, want %d", getResp.FileID, id)
	}
	if getResp.BoardKey != "051-7845" {
		t.Errorf("board_key = %q, want 051-7845", getResp.BoardKey)
	}
	if getResp.Format != ".brd" {
		t.Errorf("format = %q, want .brd", getResp.Format)
	}
	want := []string{"PP3V3_S0", "GND", "PP1V8_SUS"}
	if len(getResp.Nets) != len(want) {
		t.Fatalf("nets = %v, want %v", getResp.Nets, want)
	}
	for i, n := range want {
		if getResp.Nets[i] != n {
			t.Errorf("nets[%d] = %q, want %q (names must pass through unchanged, no normalization)", i, getResp.Nets[i], n)
		}
	}
	if getResp.ReceivedAt != postResp.ReceivedAt {
		t.Errorf("received_at = %d, want %d (from POST)", getResp.ReceivedAt, postResp.ReceivedAt)
	}
}

// TestBoardNets_ResubmitIsIdempotent verifies that sending a list again for
// the same file overwrites in place rather than accumulating — the plan
// requires "повторная отправка того же списка... не создаёт дублей и не
// растит таблицу". board_nets.file_id is the table's PRIMARY KEY (see
// migrateV11), so a second row for the same file id is structurally
// impossible; UpsertBoardNets's ON CONFLICT DO UPDATE is what makes
// resubmission succeed instead of erroring on that constraint. This test
// exercises that path end-to-end through the HTTP handlers.
func TestBoardNets_ResubmitIsIdempotent(t *testing.T) {
	h, _, id := netsTestHandler(t)

	if w := postNets(h, id, `{"nets":["A","B"]}`); w.Code != http.StatusOK {
		t.Fatalf("first POST: code = %d", w.Code)
	}
	if w := postNets(h, id, `{"nets":["A","B"]}`); w.Code != http.StatusOK {
		t.Fatalf("second POST (same list): code = %d", w.Code)
	}
	// A different list for the same file must also overwrite, not append.
	if w := postNets(h, id, `{"nets":["C"]}`); w.Code != http.StatusOK {
		t.Fatalf("third POST (different list): code = %d", w.Code)
	}

	g := getNets(h, id)
	if g.Code != http.StatusOK {
		t.Fatalf("GET after resubmit: code = %d", g.Code)
	}
	var getResp struct {
		Nets []string `json:"nets"`
	}
	json.Unmarshal(g.Body.Bytes(), &getResp)
	if len(getResp.Nets) != 1 || getResp.Nets[0] != "C" {
		t.Fatalf("nets after resubmit = %v, want [C] (stored list must reflect the latest submission only)", getResp.Nets)
	}
}

// TestBoardNets_UnknownFile404 verifies both endpoints 404 on a file id that
// doesn't exist in the databank at all — distinct from the 204 "not yet
// populated" case, which requires the file to exist.
func TestBoardNets_UnknownFile404(t *testing.T) {
	h, _, _ := netsTestHandler(t)
	const missing = 999999

	if w := getNets(h, missing); w.Code != http.StatusNotFound {
		t.Errorf("GET unknown file: code = %d, want 404", w.Code)
	}
	if w := postNets(h, missing, `{"nets":["X"]}`); w.Code != http.StatusNotFound {
		t.Errorf("POST unknown file: code = %d, want 404", w.Code)
	}
}

func TestBoardNets_MissingNetsField400(t *testing.T) {
	h, _, id := netsTestHandler(t)
	if w := postNets(h, id, `{}`); w.Code != http.StatusBadRequest {
		t.Errorf("POST without nets field: code = %d, want 400", w.Code)
	}
	if w := postNets(h, id, `not json`); w.Code != http.StatusBadRequest {
		t.Errorf("POST bad json: code = %d, want 400", w.Code)
	}
}

// TestBoardNets_EmptyListAccepted covers a board with zero named nets — not
// an error, just an empty result once submitted.
func TestBoardNets_EmptyListAccepted(t *testing.T) {
	h, _, id := netsTestHandler(t)
	if w := postNets(h, id, `{"nets":[]}`); w.Code != http.StatusOK {
		t.Fatalf("POST empty nets: code = %d body=%s", w.Code, w.Body.String())
	}
	g := getNets(h, id)
	if g.Code != http.StatusOK {
		t.Fatalf("GET after empty POST: code = %d, want 200 (list was submitted, just empty)", g.Code)
	}
}

// TestBoardNets_BoardKeyResolution verifies that board_key in the GET
// response returns the library directory name (leading path segment), not
// board_number or manufacturer. The directory name is what catalog/
// assets_importer.py::platform_board_key produces and what consumers use
// for platform identification. Board_number may name a different board
// for daughter-boards filed under their parent's directory.
func TestBoardNets_BoardKeyResolution(t *testing.T) {
	h, db, _ := netsTestHandler(t)

	tests := []struct {
		name         string
		path         string
		boardNumber  string
		manufacturer string
		wantBoardKey string
	}{
		{
			name:         "directory and board_number agree",
			path:         "820-01700/boardview/820-01700.bvr",
			boardNumber:  "820-01700",
			manufacturer: "Apple",
			wantBoardKey: "820-01700",
		},
		{
			name:         "board_number differs from directory (daughter board)",
			path:         "820-2223/boardview/820-2299 AUDIO board.brd",
			boardNumber:  "820-2299",
			manufacturer: "Apple",
			wantBoardKey: "820-2223", // directory wins
		},
		{
			name:         "empty board_number uses directory",
			path:         "A2941/boardview/MacBook Air M2 15.3' A2941 PCB layer.pcb",
			boardNumber:  "",
			manufacturer: "Apple",
			wantBoardKey: "A2941",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := db.InsertFile(&databank.FileRecord{
				Path:             tt.path,
				Filename:         "test.brd",
				Extension:        ".brd",
				FileType:         "board",
				BoardNumber:      tt.boardNumber,
				Manufacturer:     tt.manufacturer,
				ResolutionStatus: "resolved",
			})
			if err != nil {
				t.Fatalf("InsertFile: %v", err)
			}

			w := postNets(h, id, `{"nets":["NET"]}`)
			if w.Code != http.StatusOK {
				t.Fatalf("POST: code = %d", w.Code)
			}

			g := getNets(h, id)
			if g.Code != http.StatusOK {
				t.Fatalf("GET: code = %d", g.Code)
			}

			var getResp struct {
				BoardKey string `json:"board_key"`
			}
			if err := json.Unmarshal(g.Body.Bytes(), &getResp); err != nil {
				t.Fatalf("decode GET response: %v", err)
			}

			if getResp.BoardKey != tt.wantBoardKey {
				t.Errorf("board_key = %q, want %q", getResp.BoardKey, tt.wantBoardKey)
			}
		})
	}
}
