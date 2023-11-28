package model

import (
	"gorgany/app/core"
)

type Domain[T any] struct {
	DomainMeta
}

func (thiz *Domain[T]) Query() core.IOrm[T] {
	panic("Implement me in child struct. See doc...") // todo add doc url
}

func (thiz *Domain[T]) GetDomainMeta() core.IDomainMeta {
	return &thiz.DomainMeta
}

func (thiz *Domain[T]) Clone() *T {
	domain := thiz.Domain.(*core.IDomain[T])
	copiedDomain := *domain
	copiedDomain.GetDomainMeta().SetDomain(&copiedDomain)

	concreteDomain := copiedDomain.(T)
	return &concreteDomain
}
