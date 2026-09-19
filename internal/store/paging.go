package store

import "github.com/jackc/pgx/v5"

// readOnlyTx gives a read of a parent row and its children one snapshot, so a
// concurrent save is seen entirely or not at all.
var readOnlyTx = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}

// pageOf applies the keyset-pagination trick shared by the list queries: the
// query fetches limit+1 rows, so a result longer than limit means there is a
// next page. It returns the page (at most limit rows) and whether the caller
// must build a next cursor.
func pageOf[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}
