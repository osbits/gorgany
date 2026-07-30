// Package builtin registers every datasource driver that ships with the framework.
//
// It is the convenience import — both engines, one line:
//
//	import _ "github.com/osbits/gorgany/db/sql/driver/builtin"
//
// It no longer registers them itself; it imports driver/postgres and driver/mysql, each of
// which registers one. The split exists because provider.DbProvider used to import this
// package unconditionally, so a Postgres-only app linked gorm.io/driver/mysql,
// go-sql-driver/mysql and filippo.io/edwards25519 with no way to opt out. DbProvider now
// imports nothing and the app chooses: the one engine it uses, or this package for both.
//
// An app adding an engine of its own calls driver.Register("<name>", ctor) from its
// provider's Register phase; it does not need this package.
package builtin

import (
	// Imported for their registration side effects.
	_ "github.com/osbits/gorgany/db/sql/driver/mysql"
	_ "github.com/osbits/gorgany/db/sql/driver/postgres"
)

// Driver names as written under `databases.<name>.driver` in the app config.
//
// Kept here for anything that referenced them before the split. driver/postgres.Name and
// driver/mysql.Name are the same values, and are the ones to reach for now: they are
// available without linking the other engine.
const (
	PostgresGorm = "postgres_gorm"
	MySQLGorm    = "mysql_gorm"
)
