package cite

import (
	"strings"
	"testing"
)

func sampleItem() Item {
	return Item{
		Key:      "smith2020attention",
		ItemType: "journalArticle",
		Title:    "Attention Is All You Need",
		Authors:  []string{"Smith, John", "Doe, Jane"},
		Year:     2020,
		Journal:  "Journal of Machine Learning",
		Volume:   "12",
		Issue:    "3",
		Pages:    "1-10",
		DOI:      "10.1000/xyz",
		Language: "en",
	}
}

func TestGBTNumeric(t *testing.T) {
	it := sampleItem()
	got := Format(it, StyleGBTNumeric)
	for _, want := range []string{"SMITH J", "DOE J", "Attention Is All You Need[J]", "Journal of Machine Learning", "2020", "12(3)", "1-10", "DOI: 10.1000/xyz"} {
		if !strings.Contains(got, want) {
			t.Fatalf("GB/T citation missing %q: %s", want, got)
		}
	}
	if mark := InText(it, 1, StyleGBTNumeric); mark != "[1]" {
		t.Fatalf("GB/T hover mark = %q", mark)
	}
	if mark := InText(it, 2, StyleIEEE); mark != "[2]" {
		t.Fatalf("IEEE mark = %q", mark)
	}
	if mark := InText(it, 3, StyleVancouver); mark != "(3)" {
		t.Fatalf("Vancouver mark = %q", mark)
	}
}

func TestGBTAuthorsOverThree(t *testing.T) {
	it := sampleItem()
	it.Authors = []string{"张三", "李四", "王五", "赵六"}
	it.Language = "zh"
	it.ItemType = "book"
	it.Title = "知识库系统"
	it.Journal = ""
	it.Publisher = "科学出版社"
	it.Place = "北京"
	got := Format(it, StyleGBTNumeric)
	if !strings.Contains(got, "张三, 李四, 王五, 等") {
		t.Fatalf("GB/T should use 等 for >3 authors: %s", got)
	}
	if !strings.Contains(got, "[M]") || !strings.Contains(got, "北京: 科学出版社, 2020") {
		t.Fatalf("book format wrong: %s", got)
	}
}

func TestAPAAuthorDate(t *testing.T) {
	it := sampleItem()
	got := Format(it, StyleAPA)
	for _, want := range []string{"Smith, J.", "& Doe, J.", "(2020).", "Attention Is All You Need.", "Journal of Machine Learning", "https://doi.org/10.1000/xyz"} {
		if !strings.Contains(got, want) {
			t.Fatalf("APA citation missing %q: %s", want, got)
		}
	}
	if mark := InText(it, 1, StyleAPA); !strings.Contains(mark, "Smith et al.") || !strings.Contains(mark, "2020") {
		t.Fatalf("APA in-text = %q", mark)
	}
}

func TestIEEE(t *testing.T) {
	it := sampleItem()
	got := Format(it, StyleIEEE)
	for _, want := range []string{"J. Smith", "J. Doe", "\"Attention Is All You Need,\"", "vol. 12", "no. 3", "pp. 1-10", "2020"} {
		if !strings.Contains(got, want) {
			t.Fatalf("IEEE citation missing %q: %s", want, got)
		}
	}
}

func TestBibTeX(t *testing.T) {
	it := sampleItem()
	it.Key = ""
	it.Title = `Attention & 100% "quoted"`
	got := BibTeX(it)
	for _, want := range []string{"@article{", "author = {Smith, John and Doe, Jane}", `Attention \& 100\% "quoted"`, "doi = {10.1000/xyz}"} {
		if !strings.Contains(got, want) {
			t.Fatalf("BibTeX missing %q:\n%s", want, got)
		}
	}
}

func TestBuildReferencesDedupes(t *testing.T) {
	a := sampleItem()
	b := sampleItem()
	b.Key = "other"
	b.Title = "Other Paper"
	refs := BuildReferences([]Item{a, a, b}, StyleGBTNumeric)
	if len(refs) != 2 {
		t.Fatalf("expected 2 references, got %d", len(refs))
	}
	if refs[0].Mark != "[1]" || refs[1].Mark != "[2]" {
		t.Fatalf("unexpected marks: %+v", refs)
	}
}
