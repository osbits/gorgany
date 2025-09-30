package auth

import (
	"context"
	"github.com/gorganyio/gorgany/app/core"
)

type AuthContext struct {
	AuthStrategies map[string]core.IAuthStrategy
}

func (thiz *AuthContext) Init() {
	thiz.AuthStrategies = make(map[string]core.IAuthStrategy)
}

func (thiz *AuthContext) RegisterAuthStrategy(name string, strategy core.IAuthStrategy) {
	if thiz.AuthStrategies == nil {
		thiz.AuthStrategies = make(map[string]core.IAuthStrategy)
	}
	thiz.AuthStrategies[name] = strategy
}

func (thiz *AuthContext) GetAuthStrategy(strategyName ...string) core.IAuthStrategy {
	name := core.DefaultKeyInRegistrar
	if len(strategyName) > 0 {
		name = strategyName[0]
	}

	return thiz.AuthStrategies[name]
}

func (thiz *AuthContext) Strategy(strategyName ...string) core.IAuthStrategy {
	return thiz.GetAuthStrategy(strategyName...)
}

// ResolveAuthStrategyByContext :
// param ctx must be context with value and has key core.MessageContextKey with core.IMessageContext value
func (thiz *AuthContext) ResolveAuthStrategyByContext(ctx context.Context) core.IAuthStrategy {
	authStrategies := thiz.AuthStrategies
	for key := range authStrategies {
		if key == core.DefaultKeyInRegistrar {
			continue
		}
		strategy := authStrategies[key]
		if strategy.IsRequestMadeWithStrategy(ctx) {
			return strategy
		}
	}

	return authStrategies[core.DefaultKeyInRegistrar]
}
