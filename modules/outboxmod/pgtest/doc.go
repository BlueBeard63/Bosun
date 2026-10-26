// Package pgtest runs outboxmod's relay tests against a real Postgres. It is a
// separate module so the root bosun module does not depend on a Postgres
// driver. The tests skip unless BOSUN_POSTGRES_DSN is set; CI's integration
// job provides one and runs them against the checked-out bosun via go.work.
package pgtest
