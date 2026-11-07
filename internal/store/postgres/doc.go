// Package postgres implements contract.Store on Postgres.
//
// Task 5 owns this package and the migrations folder. It uses
// database/sql with the github.com/lib/pq driver, which has no other
// dependencies. New runs any pending migration before it returns.
//
// Tests need a real database and sit behind the "pg" build tag; see
// postgres_test.go.
package postgres
