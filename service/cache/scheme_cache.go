package cache

import (
	err2 "github.com/osbits/gorgany/err"
	"gorm.io/gorm/schema"
	"sync"
)

var domainSchemeCache *DomainSchemeCache

type DomainSchemeCache struct {
	cache *sync.Map
	namer schema.NamingStrategy
}

func GetDomainSchemeCache() *DomainSchemeCache {
	if domainSchemeCache == nil {
		return &DomainSchemeCache{
			cache: &sync.Map{},
			namer: schema.NamingStrategy{},
		}
	}
	return domainSchemeCache
}

func (thiz *DomainSchemeCache) ParseDomain(domain any) *schema.Schema {
	s, err := schema.Parse(domain, thiz.cache, schema.NamingStrategy{})
	if err != nil {
		err2.HandleErrorWithStacktrace(err)
		return nil
	}
	return s
}
