package kernel

import modules "github.com/rzbdz/newgate/component"

// staticLoader hands the kernel a fixed component table (the removal matrix needs
// one graph per missing module).
type staticLoader []modules.Component

func (l staticLoader) Load() ([]modules.Component, error) { return l, nil }
