package parse

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestParseMinerUZipWithImages(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	add("result/full.md", "# Test\n\n![fig](images/a.png)\n")
	add("result/images/a.png", "PNG")
	add("result/images/b.jpg", "JPG")
	add("result/other.txt", "ignore")
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	md, images, err := parseMinerUZip(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(md, "![fig](images/a.png)") {
		t.Fatalf("unexpected markdown: %s", md)
	}
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
	if images[0].Name != "a.png" || images[0].MIME != "image/png" {
		t.Fatalf("unexpected image: %+v", images[0])
	}
	if string(images[0].Data) != "PNG" {
		t.Fatalf("unexpected image data: %q", string(images[0].Data))
	}
}

func TestParseMinerUZipWithoutMarkdown(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("result/images/a.png")
	_, _ = w.Write([]byte("PNG"))
	_ = zw.Close()
	if _, _, err := parseMinerUZip(buf.Bytes()); err == nil {
		t.Fatal("expected error for zip without markdown")
	}
}
