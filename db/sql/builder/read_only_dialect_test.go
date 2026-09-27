package builder_test

import (
	"testing"

	"github.com/osbits/gorgany/v2/db/sql/builder"
	dbCore "github.com/osbits/gorgany/v2/db/sql/core"
	mysql "github.com/osbits/gorgany/v2/db/sql/gorm/mysql/v2"
	postgres "github.com/osbits/gorgany/v2/db/sql/gorm/postgres/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readOnlyGoldenDialects are goldenDialects with ReadOnly set, by the same names.
var readOnlyGoldenDialects = map[string]func() dbCore.SQLDialect{
	"postgres": func() dbCore.SQLDialect { return &postgres.PostgresDialect{ReadOnly: true} },
	"mysql":    func() dbCore.SQLDialect { return &mysql.MySQLDialect{ReadOnly: true} },
	"mysql+unfaithful-upsert": func() dbCore.SQLDialect {
		return &mysql.MySQLDialect{AllowUnfaithfulUpsert: true, ReadOnly: true}
	},
}

// TestReadOnlyDialectsRenderEveryReadOfTheGoldenCorpus: a read_only datasource's dialect
// refuses every write in the corpus and renders every read exactly as the dialect without the
// flag does — SQL, args and refusals alike — which TestPostgresAndMySQLOutputMatchesGolden
// pins against testdata/pg_mysql.golden. So the flag cannot change a read, and the golden
// file needs no entries of its own for it.
func TestReadOnlyDialectsRenderEveryReadOfTheGoldenCorpus(t *testing.T) {
	require.Len(t, readOnlyGoldenDialects, len(goldenDialects))

	reads, writes := 0, 0
	for _, c := range dialectCorpus() {
		for _, d := range goldenDialects {
			if d.upsertOnly && !c.upsert {
				continue
			}
			readOnly, ok := readOnlyGoldenDialects[d.name]
			require.Truef(t, ok, "no read-only counterpart for %s", d.name)

			q := c.build(builder.New(d.dialect())).Build()
			if q.Insert == nil && q.Update == nil && q.Delete == nil {
				reads++
				assert.Equalf(t, renderDialectCase(c, d.dialect()), renderDialectCase(c, readOnly()),
					"%s [%s] must render as it does without read_only", c.name, d.name)
				continue
			}

			writes++
			sql, args, err := c.build(builder.New(readOnly())).ToSQL()
			assert.ErrorIsf(t, err, dbCore.ErrReadOnly, "%s [%s] is a write", c.name, d.name)
			assert.Emptyf(t, sql, "%s [%s]", c.name, d.name)
			assert.Nilf(t, args, "%s [%s]", c.name, d.name)
		}
	}
	assert.NotZero(t, reads)
	assert.NotZero(t, writes)
}
