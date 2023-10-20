package model

import "gorgany/app/core"

type DomainMeta struct {
	Loaded bool
	Table  string
	Driver core.DbType
	Db     string
}

func (thiz *DomainMeta) SetLoaded(loaded bool) {
	thiz.Loaded = loaded
}

func (thiz *DomainMeta) SetTable(table string) {
	thiz.Table = table
}

func (thiz *DomainMeta) SetDriver(driver core.DbType) {
	thiz.Driver = driver
}

func (thiz *DomainMeta) SetDb(db string) {
	thiz.Db = db
}
