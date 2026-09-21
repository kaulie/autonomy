// Package db is Autonomy's database module: the storage engines that back the
// persistence contract, kept out of the root package so all database code lives
// in one place.
//
// The Store ports themselves stay in the root package (src/store.go) — they are
// the contract the rest of the runtime codes against, so the domain owns them —
// and the engine SPI and its registry stay there too (src/store_engine.go). What
// lives here is everything that talks to a database:
//
//   - the sqlite engine (sqlite_*.go), the built-in, file-backed default;
//   - the postgres engine (postgres_*.go), pluggable via AUTONOMY_STORE_ENGINE;
//   - each engine's schema and migrations, next to its SQL;
//   - the row-encoding and dialect-neutral record helpers both engines share
//     (store_row_text.go, turn_query_page.go).
//
// An engine maps one dialect to the ports: it registers itself with the root
// registry from its init, and OpenStore/OpenDefaultStore (root) dispatch to it by
// name. The root package never names a driver or a concrete engine; this package
// imports the root package for the ports and the record types they carry, so the
// dependency runs db -> autonomy and never back.
//
// Because an engine reads the root package for the contract's types, every file
// here dot-imports the root package and names those types unqualified, exactly as
// they were written when the engines lived in the root package; only the package
// clause changed.
package db
