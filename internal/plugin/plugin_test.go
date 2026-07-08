package plugin

import (
	"strings"
	"testing"
)

// stubPlugin is a minimal Plugin for registry tests.
type stubPlugin struct{ domain string }

func (s stubPlugin) Domain() string                        { return s.domain }
func (s stubPlugin) Detect(home string) (Detection, error) { return Detection{}, nil }
func (s stubPlugin) Parse(content []byte) ([]Block, error) { return nil, nil }
func (s stubPlugin) Analyze(blocks []Block) []Finding      { return nil }
func (s stubPlugin) ApplyRepairs(b []Block, a []Finding) ([]Block, error) {
	return b, nil
}
func (s stubPlugin) StarterQuestions() []Question      { return nil }
func (s stubPlugin) Starter(a Answers) ([]byte, error) { return nil, nil }
func (s stubPlugin) Describe(b Block) string           { return "" }

// AC-plugin-registry: the registry resolves a registered domain.
func TestRegistryResolve(t *testing.T) {
	r := NewRegistry()
	r.Register(stubPlugin{domain: "zsh"})
	p, err := r.Get("zsh")
	if err != nil {
		t.Fatalf("Get(zsh): %v", err)
	}
	if p.Domain() != "zsh" {
		t.Errorf("resolved wrong plugin: %q", p.Domain())
	}
}

// AC-plugin-registry: an unknown domain errors.
func TestRegistryUnknownDomainErrors(t *testing.T) {
	r := NewRegistry()
	r.Register(stubPlugin{domain: "zsh"})
	if _, err := r.Get("gitconfig"); err == nil {
		t.Fatal("Get(gitconfig) on a zsh-only registry did not error")
	} else if !strings.Contains(err.Error(), "gitconfig") {
		t.Errorf("error does not name the unknown domain: %v", err)
	}
}

// AC-plugin-registry: duplicate registration panics (programmer error).
func TestRegistryDuplicatePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	r := NewRegistry()
	r.Register(stubPlugin{domain: "zsh"})
	r.Register(stubPlugin{domain: "zsh"})
}

// Reassemble is the exact concatenation of Block.Raw.
func TestReassembleConcatsRaw(t *testing.T) {
	blocks := []Block{
		{Raw: []byte("a\n\n")},
		{Raw: []byte("")},
		{Raw: []byte("b")},
	}
	if got := string(Reassemble(blocks)); got != "a\n\nb" {
		t.Errorf("Reassemble = %q, want %q", got, "a\n\nb")
	}
}
