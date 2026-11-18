package sandbox

import (
	"bytes"
	"context"
	"fmt"

	"github.com/tetratelabs/wazero/api"
)

// Instance is a live "reactor" plugin: built with -buildmode=c-shared, it
// exports functions (Go's //go:wasmexport) instead of running main once.
// State lives as long as the instance, so give each caller its own.
type Instance struct {
	mod    api.Module
	limits Limits
}

// Instance starts a reactor instance; it runs the module's _initialize.
func (p *Plugin) Instance(ctx context.Context) (*Instance, error) {
	out := &capped{limit: p.limits.MaxOutput, stop: func() {}}
	cfg := p.moduleConfig(bytes.NewReader(nil), out.stream(&out.stdout), out.stream(&out.stderr)).
		WithName("").WithStartFunctions("_initialize")
	ictx, cancel := context.WithTimeout(ctx, p.limits.Timeout)
	defer cancel()
	mod, err := p.rt.InstantiateModule(ictx, p.compiled, cfg)
	if err != nil {
		return nil, fmt.Errorf("%s: starting instance: %w", p.name, err)
	}
	return &Instance{mod: mod, limits: p.limits}, nil
}

// Call runs an exported function under the plugin's timeout.
func (in *Instance) Call(ctx context.Context, name string, args ...uint64) ([]uint64, error) {
	fn := in.mod.ExportedFunction(name)
	if fn == nil {
		return nil, fmt.Errorf("no exported function %q", name)
	}
	ctx, cancel := context.WithTimeout(ctx, in.limits.Timeout)
	defer cancel()
	return fn.Call(ctx, args...)
}

// Close ends the instance.
func (in *Instance) Close(ctx context.Context) error { return in.mod.Close(ctx) }
