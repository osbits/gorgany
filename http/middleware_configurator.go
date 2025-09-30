package http

import "github.com/gorganyio/gorgany/app/core"

func NewMiddlewareConfigBuilder() core.IMiddlewareConfigBuilder {
	return &MiddlewareConfigBuilder{
		middlewareConfig: &MiddlewareConfig{},
	}
}

type MiddlewareConfigBuilder struct {
	middlewareConfig *MiddlewareConfig
}

func (thiz *MiddlewareConfigBuilder) WithPattern(pattern string) core.IMiddlewareConfigBuilder {
	thiz.middlewareConfig.pattern = pattern
	return thiz
}

func (thiz *MiddlewareConfigBuilder) WithExcludePatterns(patterns []string) core.IMiddlewareConfigBuilder {
	thiz.middlewareConfig.excludePatterns = patterns
	return thiz
}

func (thiz *MiddlewareConfigBuilder) AsFilter() core.IMiddlewareConfigBuilder {
	thiz.middlewareConfig.isFilter = true
	return thiz
}

func (thiz *MiddlewareConfigBuilder) WithMiddleware(mw core.IMiddleware) core.IMiddlewareConfigBuilder {
	thiz.middlewareConfig.middleware = mw
	return thiz
}

func (thiz *MiddlewareConfigBuilder) Build() core.IMiddlewareConfig {
	if thiz.middlewareConfig == nil {
		thiz.middlewareConfig = &MiddlewareConfig{}
	}
	return thiz.middlewareConfig
}

type MiddlewareConfig struct {
	pattern         string
	excludePatterns []string
	isFilter        bool
	middleware      core.IMiddleware
}

func (thiz *MiddlewareConfig) GetPattern() string {
	return thiz.pattern
}

func (thiz *MiddlewareConfig) GetExcludePatterns() []string {
	return thiz.excludePatterns
}

func (thiz *MiddlewareConfig) IsFilter() bool {
	return thiz.isFilter
}

func (thiz *MiddlewareConfig) GetMiddleware() core.IMiddleware {
	return thiz.middleware
}
