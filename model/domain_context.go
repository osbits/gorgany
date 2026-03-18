package model

type DomainContext struct {
	domains map[string]any
}

func (thiz *DomainContext) RegisterDomain(key string, domain interface{}) {
	if thiz.domains == nil {
		thiz.domains = make(map[string]interface{})
	}
	thiz.domains[key] = domain
}

func (thiz *DomainContext) GetDomains() map[string]interface{} {
	return thiz.domains
}
