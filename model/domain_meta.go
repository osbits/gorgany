package model

import "git.qix.sx/gorgany/gorgany.git/app/core"

type DomainMeta struct {
	Loaded   bool
	Table    string
	Driver   core.DbType
	Original any
	Domain   any
}

func (thiz *DomainMeta) SetLoaded(loaded bool) {
	thiz.Loaded = loaded
}

func (thiz *DomainMeta) GetLoaded() bool {
	return thiz.Loaded
}

func (thiz *DomainMeta) SetTable(table string) {
	thiz.Table = table
}

func (thiz *DomainMeta) GetTable() string {
	return thiz.Table
}

func (thiz *DomainMeta) SetDriver(driver core.DbType) {
	thiz.Driver = driver
}

func (thiz *DomainMeta) GetDriver() core.DbType {
	return thiz.Driver
}

func (thiz *DomainMeta) SetOriginal(original any) {
	thiz.Original = original
}

func (thiz *DomainMeta) GetOriginal() any {
	return thiz.Original
}

func (thiz *DomainMeta) SetDomain(domain any) {
	thiz.Domain = domain
}

func (thiz *DomainMeta) GetDomain() any {
	return thiz.Domain
}

//func (thiz *DomainMeta) HasChanges() bool {
//
//}
