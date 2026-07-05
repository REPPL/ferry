package config

import "testing"

func TestProbeUnknownAssetKey(t *testing.T) {
	cfg, err := parseAgents([]byte("[agents.asset.hooks]\ntagret = \".x\"\n"))
	t.Logf("err=%v cfg.asset=%#v", err, cfg.asset)
	cfg2, err2 := parseAgents([]byte("[agents]\nbogus = 1\n"))
	t.Logf("top-level unknown: err=%v cfg=%#v", err2, cfg2)
}
