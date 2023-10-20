package model

type DomainMeta struct {
	Loaded bool
	Table  string
}

func (thiz *DomainMeta) SetLoaded(loaded bool) {
	thiz.Loaded = loaded
}

func (thiz *DomainMeta) SetTable(table string) {
	thiz.Table = table
}
