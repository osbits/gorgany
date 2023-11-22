package postgres

type Raw struct {
	sql        string
	tableAlias string
	args       []any
}

func NewRaw(sql string, tableAlias string, args ...any) *Raw {
	return &Raw{
		sql:        sql,
		tableAlias: tableAlias,
		args:       args,
	}
}
