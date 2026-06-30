package secret

import "testing"

func hasRoute(routes []Route, r Route) bool {
	for _, x := range routes {
		if x == r {
			return true
		}
	}
	return false
}

func TestGate_SecretBlockedFromBothSharedAndLocal(t *testing.T) {
	d := GateText(fakePEMKey)
	if !d.BlockedFromRepo {
		t.Fatalf("expected secret blocked from repo, got %+v", d)
	}
	if hasRoute(d.AllowedRoutes, RouteShared) {
		t.Errorf("secret must NOT be routable to shared")
	}
	if hasRoute(d.AllowedRoutes, RouteLocal) {
		t.Errorf("secret must NOT be routable to local (local/ is in the repo worktree)")
	}
	if !hasRoute(d.AllowedRoutes, RouteReject) || !hasRoute(d.AllowedRoutes, RouteSecretStore) {
		t.Errorf("secret should offer reject + secret-store only, got %v", d.AllowedRoutes)
	}
}

func TestGate_CleanChangeAllowsNormalRoutes(t *testing.T) {
	d := GateText("export EDITOR=nvim\nalias ll='ls -al'\n")
	if d.BlockedFromRepo {
		t.Fatalf("clean change must not be blocked, got %+v", d)
	}
	if !hasRoute(d.AllowedRoutes, RouteShared) || !hasRoute(d.AllowedRoutes, RouteLocal) {
		t.Errorf("clean change should offer shared + local, got %v", d.AllowedRoutes)
	}
	if hasRoute(d.AllowedRoutes, RouteSecretStore) {
		t.Errorf("clean change should not offer secret-store, got %v", d.AllowedRoutes)
	}
}

func TestGate_ValueDomainBlocks(t *testing.T) {
	d := GateValue(fakePEMKey)
	if !d.BlockedFromRepo {
		t.Errorf("opaque value with a secret must be blocked, got %+v", d)
	}
}

func TestIsBlockedFromRepo(t *testing.T) {
	if !IsBlockedFromRepo(fakePEMKey) {
		t.Errorf("expected IsBlockedFromRepo true for PEM key")
	}
	if IsBlockedFromRepo("export PATH=$HOME/bin:$PATH") {
		t.Errorf("expected IsBlockedFromRepo false for clean line")
	}
}
