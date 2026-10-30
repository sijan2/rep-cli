package commands

import (
	"context"

	"github.com/repplus/rep-cli/internal/jev"
	"github.com/repplus/rep-cli/internal/jevdom"
	"github.com/repplus/rep-cli/internal/output"
)

type jevDOMDependencies struct {
	loadConfig  func() (jev.Config, error)
	browser     func(context.Context, string) (jevdom.Browser, error)
	evaluator   func(jev.Config) jevdom.Evaluator
	asker       func(jev.Config) jev.Asker
	cache       func() *jevdom.Cache
	selectBatch func(context.Context, jevdom.Browser, jev.Config, []jevdom.Options) ([]jevdom.Result, error)
}

func defaultJevDOMDependencies() jevDOMDependencies {
	return jevDOMDependencies{
		loadConfig: jev.LoadConfig,
		browser: func(ctx context.Context, name string) (jevdom.Browser, error) {
			client, err := connectBrowser(ctx, name)
			if err != nil {
				return nil, output.NewAgentError(output.ErrCodeBrowserUnavailable, "", "cannot connect to the selected browser bridge: "+err.Error(), "rep tabs", "rep tabs")
			}
			return client, nil
		},
		evaluator:   func(config jev.Config) jevdom.Evaluator { return jev.NewClient(config) },
		asker:       func(config jev.Config) jev.Asker { return jev.NewClient(config) },
		cache:       jevdom.DefaultCache,
		selectBatch: hostSelectBatch,
	}
}
