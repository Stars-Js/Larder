package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseZoteroPrefsFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "prefs.js")
	content := "// Mozilla User Preferences\n" +
		`user_pref("extensions.zotero.dataDir", "E:\\backup\\Zotero");` + "\n" +
		`user_pref("extensions.zotero.baseAttachmentPath", "E:\\backup\\Zotero\\attachments");` + "\n"
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := parseZoteroPrefsFile(p); got != `E:\backup\Zotero` {
		t.Fatalf("data dir = %q", got)
	}
}

func TestParseZoteroPrefsMissing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "prefs.js")
	if err := os.WriteFile(p, []byte(`user_pref("browser.startup.homepage", "about:blank");`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := parseZoteroPrefsFile(p); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
	if got := parseZoteroPrefsFile(filepath.Join(dir, "missing.js")); got != "" {
		t.Fatalf("missing file should be empty, got %q", got)
	}
}

func TestDefaultZoteroDataDirEnv(t *testing.T) {
	t.Setenv("ZOTERO_DATA_DIR", `D:\ZoteroData`)
	if got := DefaultZoteroDataDir(); got != `D:\ZoteroData` {
		t.Fatalf("env data dir = %q", got)
	}
}
