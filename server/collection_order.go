package server

import (
	"database/sql"
	"errors"
)

// Row order inside a collection: `position`, then created_at and id for ties.
// Undo receipts name a row's neighbours rather than its number, because a
// renumbering (legacy ties, an exhausted gap) changes every number while
// keeping the order, and an old number then points at the wrong slot.

// neighbour is a row next to another, with its placement counter when it
// was recorded: a neighbour whose counter changed has moved since and no
// longer marks the slot.
type neighbour struct {
	ID        string `json:"id"`
	Placement int64  `json:"placement"`
}

// rowNeighbours returns the rows directly before and after rowID in colID's
// order, leaving rowID itself out. An empty ID means none: rowID is first or
// last.
func rowNeighbours(tx *sql.Tx, colID, rowID string) (prev, next neighbour, err error) {
	var p, n sql.NullString
	var pp, np sql.NullInt64
	err = tx.QueryRow(`SELECT prev, prev_placement, next, next_placement FROM (
		SELECT id, LAG(id) OVER ordered AS prev, LAG(placement) OVER ordered AS prev_placement,
			LEAD(id) OVER ordered AS next, LEAD(placement) OVER ordered AS next_placement
		FROM pages WHERE parent_id = ? AND trashed_at IS NULL
		WINDOW ordered AS (ORDER BY position, created_at, id)
	) WHERE id = ?`, colID, rowID).Scan(&p, &pp, &n, &np)
	return neighbour{p.String, pp.Int64}, neighbour{n.String, np.Int64}, err
}

// renumberRows restores integer spacing in colID without changing the order.
// A row's placement counter is kept: its place among the others is the same,
// so an undo receipt for it stays valid (the trigger adds one; this takes it
// back first).
func renumberRows(tx *sql.Tx, colID string) error {
	_, err := tx.Exec(`WITH ordered AS (
		SELECT id, ROW_NUMBER() OVER (ORDER BY position, created_at, id) AS pos
		FROM pages WHERE parent_id = ? AND trashed_at IS NULL
	) UPDATE pages SET position = ordered.pos,
		placement = CASE WHEN pages.position IS NOT ordered.pos THEN pages.placement - 1 ELSE pages.placement END
	FROM ordered WHERE pages.id = ordered.id`, colID)
	return err
}

// slotNear returns a position for rowID directly before or after anchorID in
// colID, renumbering once when no number fits between the neighbours.
// sql.ErrNoRows means the anchor is not a row of colID.
func slotNear(tx *sql.Tx, colID, rowID, anchorID string, after bool) (float64, error) {
	try := func() (float64, bool, error) {
		var anchor float64
		var prev, next sql.NullFloat64
		err := tx.QueryRow(`SELECT position, prev, next FROM (
			SELECT id, position, LAG(position) OVER ordered AS prev, LEAD(position) OVER ordered AS next
			FROM pages WHERE parent_id = ? AND trashed_at IS NULL AND id != ?
			WINDOW ordered AS (ORDER BY position, created_at, id)
		) WHERE id = ?`, colID, rowID, anchorID).Scan(&anchor, &prev, &next)
		if err != nil {
			return 0, false, err
		}
		lower, upper := prev.Float64, anchor
		if after {
			lower, upper = anchor, next.Float64
			if !next.Valid {
				return anchor + 1, anchor+1 > anchor, nil
			}
		} else if !prev.Valid {
			return anchor - 1, anchor-1 < anchor, nil
		}
		mid := lower/2 + upper/2
		return mid, lower < mid && mid < upper, nil
	}
	pos, fits, err := try()
	if err != nil || fits {
		return pos, err
	}
	if err := renumberRows(tx, colID); err != nil {
		return 0, err
	}
	pos, _, err = try()
	return pos, err
}

// restoreSlot finds the slot rowID had between its recorded neighbours:
// first if it had none before it, after the earlier neighbour if that one has
// not moved since, last if it had none after it, before the later neighbour
// if that one has not moved. ok is false when no recorded neighbour can still
// say where the slot was; the undo is then refused rather than guessed.
func restoreSlot(tx *sql.Tx, colID, rowID string, after, before neighbour) (pos float64, ok bool, err error) {
	unmoved := func(n neighbour) (bool, error) {
		var placement int64
		err := tx.QueryRow(`SELECT placement FROM pages WHERE id = ? AND parent_id = ? AND trashed_at IS NULL AND id != ?`,
			n.ID, colID, rowID).Scan(&placement)
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return err == nil && placement == n.Placement, err
	}
	end := func(first bool) (float64, bool, error) {
		var edge sql.NullFloat64
		q := `SELECT MAX(position) FROM pages WHERE parent_id = ? AND trashed_at IS NULL AND id != ?`
		if first {
			q = `SELECT MIN(position) FROM pages WHERE parent_id = ? AND trashed_at IS NULL AND id != ?`
		}
		if err := tx.QueryRow(q, colID, rowID).Scan(&edge); err != nil {
			return 0, false, err
		}
		if !edge.Valid {
			return 1, true, nil
		}
		if first {
			return edge.Float64 - 1, true, nil
		}
		return edge.Float64 + 1, true, nil
	}
	if after.ID == "" {
		return end(true)
	}
	if ok, err := unmoved(after); err != nil {
		return 0, false, err
	} else if ok {
		pos, err := slotNear(tx, colID, rowID, after.ID, true)
		return pos, err == nil, err
	}
	if before.ID == "" {
		return end(false)
	}
	if ok, err := unmoved(before); err != nil {
		return 0, false, err
	} else if ok {
		pos, err := slotNear(tx, colID, rowID, before.ID, false)
		return pos, err == nil, err
	}
	return 0, false, nil
}
