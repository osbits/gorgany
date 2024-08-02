package auth

import (
	"context"
	"git.qix.sx/gorgany/gorgany.git/app/core"
	"git.qix.sx/gorgany/gorgany.git/internal"
)

func Strategy(strategyName ...string) core.IAuthStrategy {
	return internal.GetApplicationContext().GetAuthStrategy(strategyName...)
}

// ResolveAuthStrategyByContext :
// param ctx must be context with value and has key core.MessageContextKey with core.IMessageContext value
func ResolveAuthStrategyByContext(ctx context.Context) core.IAuthStrategy {
	authStrategies := internal.GetApplicationContext().GetAuthStrategies()
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
