package model

import (
	"gorgany/app/core"
	"gorgany/db/orm"
)

type Domain[T core.IDomain[T]] struct {
	DomainMeta
}

func (thiz Domain[T]) Query() core.IOrm[T] {
	var emptyDomain T
	domain := &emptyDomain
	if thiz.Domain != nil {
		domain = thiz.Domain.(*T)
	}
	return orm.OrmInstance[T](domain)
}

func (thiz Domain[T]) GetDomainMeta() core.IDomainMeta {
	return &thiz.DomainMeta
}

func (thiz Domain[T]) Clone() *T {
	domain := *thiz.Domain.(*T)
	copiedDomain := domain
	copiedDomain.GetDomainMeta().SetDomain(&copiedDomain)
	return &copiedDomain
}
