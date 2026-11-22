package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCitationDefaultsAndStyle(t *testing.T) {
	c := Default()
	if got := c.Citation.Style("default").ReferenceStyle; got != "gb-t-7714-numeric" {
		t.Fatalf("default profile reference style = %q", got)
	}
	st := c.Citation.Style("zcode")
	if st.Mode != "plain" {
		t.Fatalf("zcode mode = %q, want plain", st.Mode)
	}
	if got := c.Citation.Style("default").Fields; len(got) != 4 || got[0] != "link" || got[1] != "reference" || got[2] != "file" || got[3] != "text" {
		t.Fatalf("default fields = %v", got)
	}
}

func TestCitationNormalizeFields(t *testing.T) {
	c := CitationConfig{Profiles: map[string]CitationStyle{
		"default": {Mode: "weird", Fields: []string{"reference", "text", "file", "text", "bad"}},
		"zcode":   {Fields: []string{}},
	}}
	NormalizeCitation(&c)
	d := c.Style("default")
	if d.Mode != "markdown" {
		t.Fatalf("invalid mode should fall back to markdown: %q", d.Mode)
	}
	if len(d.Fields) != 3 || d.Fields[0] != "reference" || d.Fields[1] != "text" || d.Fields[2] != "file" {
		t.Fatalf("field normalization/order failed: %v", d.Fields)
	}
	if got := c.Style("zcode").Fields; len(got) != 0 {
		t.Fatalf("plain mode should keep empty fields (mark only): %v", got)
	}
	if got := c.Style("missing").Fields; len(got) != 3 || got[0] != "reference" || got[1] != "text" || got[2] != "file" {
		t.Fatalf("missing profile should fall back to default: %v", got)
	}
}

func TestCitationConfigSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "larder.toml")
	c := Default()
	c.Citation.Profiles["default"] = CitationStyle{Mode: "plain", Fields: []string{"file", "text"}, ReferenceStyle: "apa"}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st := got.Citation.Style("default")
	if st.Mode != "plain" || len(st.Fields) != 2 || st.Fields[0] != "file" || st.Fields[1] != "text" {
		t.Fatalf("citation config did not round-trip: %+v", st)
	}
	if st.ReferenceStyle != "apa" {
		t.Fatalf("default profile style did not round-trip: %q", st.ReferenceStyle)
	}
	if b, err := os.ReadFile(path); err != nil {
		t.Fatal(err)
	} else {
		text := string(b)
		if strings.Contains(text, "[citation]\nversion") || strings.Contains(text, "[citation]\nstyle") {
			t.Fatalf("citation config should contain only profiles, got:\n%s", text)
		}
	}
}

func TestSampleConfigLoadsCitation(t *testing.T) {
	cfg, _, err := Load(filepath.Join("..", "..", "configs", "larder.toml"))
	if err != nil {
		t.Fatalf("sample config should parse: %v", err)
	}
	st := cfg.Citation.Style("zcode")
	if st.Mode != "plain" {
		t.Fatalf("sample zcode should stay plain: %+v", st)
	}
	if got := cfg.Citation.Style("default").ReferenceStyle; got != "gb-t-7714-numeric" {
		t.Fatalf("sample default profile style = %q", got)
	}
	if len(st.Fields) != 0 {
		t.Fatalf("sample zcode fields should be empty: %+v", st)
	}
}

func TestCitationLinkOrder(t *testing.T) {
	c := CitationConfig{Profiles: map[string]CitationStyle{
		"default": {Mode: "markdown", Fields: []string{"reference", "link", "file"}},
		"plain":   {Mode: "plain", Fields: []string{"reference", "link", "file"}},
	}}
	NormalizeCitation(&c)
	hover := c.Style("default").Fields
	if len(hover) != 3 || hover[0] != "link" || hover[1] != "reference" || hover[2] != "file" {
		t.Fatalf("hover link must be first: %v", hover)
	}
	plain := c.Style("plain").Fields
	if len(plain) != 3 || plain[0] != "reference" || plain[1] != "link" || plain[2] != "file" {
		t.Fatalf("plain fields should keep user order: %v", plain)
	}
}

func TestCitationEmptyFieldsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "larder.toml")
	c := Default()
	c.Citation.Profiles["default"] = CitationStyle{Mode: "markdown", Fields: []string{}}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if fields := got.Citation.Style("default").Fields; len(fields) != 0 {
		t.Fatalf("empty fields must survive save/load, got %v", fields)
	}
}

func TestCitationPerProfileReferenceStyle(t *testing.T) {
	c := CitationConfig{Profiles: map[string]CitationStyle{
		"default": {Mode: "markdown", ReferenceStyle: "apa"},
		"zcode":   {Mode: "plain", ReferenceStyle: "ieee"},
	}}
	NormalizeCitation(&c)
	if got := c.Style("default").ReferenceStyle; got != "apa" {
		t.Fatalf("default profile style = %q", got)
	}
	if got := c.Style("unknown").ReferenceStyle; got != "apa" {
		t.Fatalf("unknown profile should inherit default, got %q", got)
	}
	if got := c.Style("zcode").ReferenceStyle; got != "ieee" {
		t.Fatalf("zcode should keep its own style, got %q", got)
	}
}

func TestCitationLegacyKeysAreIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "larder.toml")
	body := `[citation]
version = 5
style = 'apa'

[citation.profiles.default]
mode = 'markdown'
fields = ['link', 'reference']
style = 'ieee'
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := Load(path)
	if err != nil {
		t.Fatalf("legacy keys should not break loading: %v", err)
	}
	if got := cfg.Citation.Style("default").ReferenceStyle; got != "ieee" {
		t.Fatalf("profile style = %q, want ieee", got)
	}
}
