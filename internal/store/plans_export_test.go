package store

import "github.com/jackc/pgx/v5/pgxpool"

// PlanTestDB wraps a pool in a DB. The external tests (package store_test) use
// it to run the plan store on a second pool that carries a query tracer, which
// the public constructor Open cannot set up.
func PlanTestDB(pool *pgxpool.Pool) *DB { return &DB{pool: pool} }
