package config

import (
	"path/filepath"
	"testing"
)

func TestCitationDefaultsAndStyle(t *testing.T) {
	c := Default()
	st := c.Citation.Style("zcode")
	if st.Mode != "plain" {
		t.Fatalf("zcode mode = %q, want plain", st.Mode)
	}
	if got := c.Citation.Style("default").Fields; len(got) != 5 || got[0] != "cite" || got[1] != "log" || got[2] != "doc" || got[3] != "file" || got[4] != "text" {
		t.Fatalf("default fields = %v", got)
	}
}

func TestCitationNormalizeFields(t *testing.T) {
	c := CitationConfig{Version: 2, Profiles: map[string]CitationStyle{
		"default": {Mode: "weird", Fields: []string{"cite", "text", "doc", "text", "bad"}},
		"zcode":   {Fields: []string{}},
	}}
	NormalizeCitation(&c)
	d := c.Style("default")
	if d.Mode != "markdown" {
		t.Fatalf("invalid mode should fall back to markdown: %q", d.Mode)
	}
	if len(d.Fields) != 3 || d.Fields[0] != "cite" || d.Fields[1] != "text" || d.Fields[2] != "doc" {
		t.Fatalf("field normalization/order failed: %v", d.Fields)
	}
	if got := c.Style("zcode").Fields; len(got) != 0 {
		t.Fatalf("explicit empty fields should stay empty: %v", got)
	}
	if got := c.Style("missing").Fields; len(got) != 3 || got[0] != "cite" || got[1] != "text" || got[2] != "doc" {
		t.Fatalf("missing profile should fall back to default: %v", got)
	}
}

func TestCitationConfigSaveLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "larder.toml")
	c := Default()
	c.Citation.Version = 2
	c.Citation.Profiles["default"] = CitationStyle{Mode: "plain", Fields: []string{"file", "text", "doc"}}
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, _, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	st := got.Citation.Style("default")
	if st.Mode != "plain" || len(st.Fields) != 3 || st.Fields[0] != "file" || st.Fields[1] != "text" || st.Fields[2] != "doc" {
		t.Fatalf("citation config did not round-trip: %+v", st)
	}
}

func TestSampleConfigLoadsCitation(t *testing.T) {
	cfg, _, err := Load(filepath.Join("..", "..", "configs", "larder.toml"))
	if err != nil {
		t.Fatalf("sample config should parse: %v", err)
	}
	st := cfg.Citation.Style("zcode")
	if st.Mode != "plain" || len(st.Fields) == 0 || st.Fields[0] != "cite" || st.Fields[1] != "log" {
		t.Fatalf("sample zcode citation = %+v", st)
	}
}
