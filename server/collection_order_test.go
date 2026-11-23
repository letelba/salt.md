package server

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// The review's case: a tied pair forces a renumbering after two other changes
// were recorded; every undo still finds its card and puts it back between the
// neighbours it had.
func TestUndoSurvivesRenumbering(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "order@example.com")
	ws := s.firstWorkspaceOf(t, uid)
	col := s.makeCollection(t, ws, uid, "Tasks", `[]`)
	other := s.makeCollection(t, ws, uid, "Elsewhere", `[]`)
	ids := map[string]string{}
	for i, name := range []string{"a", "b", "c", "d", "e", "f"} {
		ids[name] = s.makeRow(t, ws, uid, col, name, `{}`)
		s.db.Exec(`UPDATE pages SET position = ?, created_at = ? WHERE id = ?`, i+1, "2026-01-0"+string(rune('1'+i)), ids[name])
	}
	// c and d share a position, as old data can.
	s.db.Exec(`UPDATE pages SET position = 3 WHERE id = ?`, ids["d"])

	post := func(path, body string) map[string]any {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s %s: %d %s", path, body, rec.Code, rec.Body.String())
		}
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	restore := func(r map[string]any) string {
		b, _ := json.Marshal(map[string]any{"position": r["fromPosition"], "expectedPosition": r["position"],
			"expectedPlacement": r["placement"], "placement": r["fromPlacement"], "after": r["fromAfter"], "before": r["fromBefore"]})
		return string(b)
	}
	order := func() string {
		t.Helper()
		rows, _ := s.db.Query(`SELECT title FROM pages WHERE parent_id = ? AND trashed_at IS NULL ORDER BY position, created_at, id`, col)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var title string
			rows.Scan(&title)
			out = append(out, title)
		}
		return strings.Join(out, "")
	}
	start := order()
	if start != "abcdef" {
		t.Fatalf("start = %s", start)
	}
	moved := post("/api/collections/"+col+"/rows/move", `{"rowId":"`+ids["b"]+`","collectionId":"`+other+`"}`)
	reordered := post("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["f"]+`","anchorId":"`+ids["a"]+`","after":false}`)
	if got := order(); got != "facde" {
		t.Fatalf("after two changes = %s", got)
	}
	// Between the tied c and d: no number fits, so the collection is renumbered.
	dropped := post("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["e"]+`","anchorId":"`+ids["c"]+`","after":true}`)
	if got := order(); got != "faced" {
		t.Fatalf("after the renumbering drop = %s", got)
	}
	post("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["e"]+`","restore":`+restore(dropped)+`}`)
	if got := order(); got != "facde" {
		t.Fatalf("undo of the drop = %s", got)
	}
	post("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["f"]+`","restore":`+restore(reordered)+`}`)
	if got := order(); got != "acdef" {
		t.Fatalf("undo of the reorder = %s", got)
	}
	post("/api/collections/"+other+"/rows/move", `{"rowId":"`+ids["b"]+`","collectionId":"`+col+`","restore":`+restore(moved)+`}`)
	if got := order(); got != start {
		t.Fatalf("undo of the move = %s, want %s", got, start)
	}
}

func TestReorderUndoFollowsOnlyUnmovedNeighbours(t *testing.T) {
	s := testServer(t)
	uid, cookie := signedIn(t, s, "neighbours@example.com")
	ws := s.firstWorkspaceOf(t, uid)
	col := s.makeCollection(t, ws, uid, "Tasks", `[]`)
	other := s.makeCollection(t, ws, uid, "Elsewhere", `[]`)
	ids := map[string]string{}
	for i, name := range []string{"a", "b", "c", "d"} {
		ids[name] = s.makeRow(t, ws, uid, col, name, `{}`)
		s.db.Exec(`UPDATE pages SET position = ?, created_at = ? WHERE id = ?`, i+1, "2026-01-0"+string(rune('1'+i)), ids[name])
	}
	call := func(path, body string) (int, map[string]any) {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Cookie", cookie)
		rec := httptest.NewRecorder()
		s.ServeHTTP(rec, req)
		var out map[string]any
		json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	ok := func(path, body string) map[string]any {
		t.Helper()
		code, out := call(path, body)
		if code != 200 {
			t.Fatalf("%s %s: %d %v", path, body, code, out)
		}
		return out
	}
	restore := func(r map[string]any) string {
		b, _ := json.Marshal(map[string]any{"position": r["fromPosition"], "expectedPosition": r["position"],
			"expectedPlacement": r["placement"], "placement": r["fromPlacement"], "after": r["fromAfter"], "before": r["fromBefore"]})
		return string(b)
	}
	order := func() string {
		rows, _ := s.db.Query(`SELECT title FROM pages WHERE parent_id = ? AND trashed_at IS NULL ORDER BY position, created_at, id`, col)
		defer rows.Close()
		out := ""
		for rows.Next() {
			var title string
			rows.Scan(&title)
			out += title
		}
		return out
	}
	reorder := func(row, anchor string, after bool) map[string]any {
		b, _ := json.Marshal(map[string]any{"rowId": ids[row], "anchorId": ids[anchor], "after": after})
		return ok("/api/collections/"+col+"/rows/reorder", string(b))
	}

	// B after D in one tab, A to the end in another, then undo B: A moved, so
	// B goes back before C, its unmoved later neighbour.
	moved := reorder("b", "d", true)
	reorder("a", "b", true)
	if got := order(); got != "cdba" {
		t.Fatalf("before undo = %s", got)
	}
	ok("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["b"]+`","restore":`+restore(moved)+`}`)
	if got := order(); got != "bcda" {
		t.Fatalf("undo followed a moved neighbour: %s", got)
	}

	// Both recorded neighbours moved: the undo is refused, not guessed.
	moved = reorder("c", "a", true) // b d a c
	reorder("b", "a", true)         // d a b c
	reorder("d", "c", true)         // a b c d
	if code, _ := call("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["c"]+`","restore":`+restore(moved)+`}`); code != 409 {
		t.Fatalf("undo with both neighbours moved: %d, want 409", code)
	}

	// A renumbering lets a card change place while keeping its number; it is
	// still a move, with a receipt that undoes it.
	s.db.Exec(`UPDATE pages SET position = 1 WHERE id IN (?, ?)`, ids["a"], ids["b"])
	s.db.Exec(`UPDATE pages SET position = 1.5 WHERE id = ?`, ids["c"])
	s.db.Exec(`UPDATE pages SET position = 9 WHERE id = ?`, ids["d"])
	if got := order(); got != "abcd" {
		t.Fatalf("setup = %s", got)
	}
	up := reorder("c", "b", false)
	if got := order(); got != "acbd" || up["moved"] != true {
		t.Fatalf("move up = %s, moved = %v", got, up["moved"])
	}
	ok("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids["c"]+`","restore":`+restore(up)+`}`)
	if got := order(); got != "abcd" {
		t.Fatalf("undo of move up = %s", got)
	}

	// The review's case: tab 2's "move C up" arrives late, after tab 1 moved C
	// up and D up twice. C is already before B, so nothing is written, and tab
	// 1 can still undo all three moves.
	if got := order(); got != "abcd" {
		t.Fatalf("setup = %s", got)
	}
	cUp := reorder("c", "b", false)  // a c b d
	dUp1 := reorder("d", "b", false) // a c d b
	dUp2 := reorder("d", "c", false) // a d c b
	var before int64
	s.db.QueryRow(`SELECT placement FROM pages WHERE id = ?`, ids["c"]).Scan(&before)
	late := reorder("c", "b", false)
	var after int64
	s.db.QueryRow(`SELECT placement FROM pages WHERE id = ?`, ids["c"]).Scan(&after)
	if late["moved"] != false || after != before || order() != "adcb" {
		t.Fatalf("the late request wrote: moved=%v placement %d→%d order %s", late["moved"], before, after, order())
	}
	for _, step := range []struct {
		row     string
		receipt map[string]any
	}{{"d", dUp2}, {"d", dUp1}, {"c", cUp}} {
		ok("/api/collections/"+col+"/rows/reorder", `{"rowId":"`+ids[step.row]+`","restore":`+restore(step.receipt)+`}`)
	}
	if got := order(); got != "abcd" {
		t.Fatalf("after undoing all three = %s", got)
	}

	// A page opened before this release sends no neighbours: it restores by
	// position, with the old staleness check.
	out := ok("/api/collections/"+col+"/rows/move", `{"rowId":"`+ids["b"]+`","collectionId":"`+other+`"}`)
	legacy, _ := json.Marshal(map[string]any{"position": out["fromPosition"], "expectedPosition": out["position"],
		"expectedPlacement": out["placement"], "placement": out["fromPlacement"]})
	ok("/api/collections/"+other+"/rows/move", `{"rowId":"`+ids["b"]+`","collectionId":"`+col+`","restore":`+string(legacy)+`}`)
	if got := order(); got != "abcd" {
		t.Fatalf("legacy undo = %s", got)
	}
}
