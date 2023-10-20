package model

import "gorgany/app/core"

type DomainMeta struct {
	Loaded bool
	Table  string
	Driver core.DbType
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
