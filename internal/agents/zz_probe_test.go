package agents

import (
	"testing"

	"github.com/REPPL/ferry/internal/config"
)

func TestProbePartialLocalOverrideOfUserMapping(t *testing.T) {
	// Merged result of shared {source=githooks,target=.githooks} + local {target=.hooks-local}
	cfg := config.AgentsConfig{
		Asset: map[string]config.AgentsAsset{
			"githooks": {Target: ".hooks-local"},
		},
	}
	got, err := ResolveAssets(cfg)
	if err != nil {
		t.Logf("ResolveAssets error: %v", err)
	} else {
		t.Logf("ResolveAssets ok: %+v", got)
	}
}
