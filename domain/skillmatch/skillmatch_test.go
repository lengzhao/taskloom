package skillmatch_test

import (
	"testing"

	"github.com/lengzhao/taskloom/domain/skillmatch"
)

func TestNamePrefixOutranksFuzzy(t *testing.T) {
	p, ok := skillmatch.Name("pdf-reader", "pdf")
	if !ok || p != 1 {
		t.Fatal("prefix", p, ok)
	}
	f, ok := skillmatch.Name("pdf-reader", "pdf-raeder")
	if !ok || f >= p {
		t.Fatal("fuzzy", f, ok)
	}
	if _, ok := skillmatch.Name("pdf-reader", "kubernetes"); ok {
		t.Fatal("unrelated name matched")
	}
}

func TestLexicalRanksNameAboveDescription(t *testing.T) {
	text := skillmatch.Text{Name: "slides-maker", Description: "生成演示文稿和幻灯片"}
	name, _ := skillmatch.Lexical(text, "slides")
	desc, ok := skillmatch.Lexical(text, "幻灯片")
	if !ok || desc >= name {
		t.Fatal(name, desc, ok)
	}
	if _, ok := skillmatch.Lexical(text, "数据库"); ok {
		t.Fatal("unrelated text matched")
	}
}

func TestSemanticThresholdAndBonus(t *testing.T) {
	a := []float32{1, 0, 0}
	if c := skillmatch.Cosine(a, []float32{1, 0, 0}); c < 0.999 {
		t.Fatal(c)
	}
	if skillmatch.Cosine(a, []float32{1, 0}) != 0 {
		t.Fatal("dimension mismatch must not score")
	}
	if _, ok := skillmatch.Semantic(0.2, false, 0.65, 0.1); ok {
		t.Fatal("distance 0.8 accepted")
	}
	s, ok := skillmatch.Semantic(0.95, true, 0.65, 0.1)
	if !ok || s != 1 {
		t.Fatal("bonus must be capped at 1", s)
	}
}

func TestBM25RanksRareTermsAndNames(t *testing.T) {
	docs := []skillmatch.Text{
		{Name: "pdf-notes", Description: "Summarize PDF documents into notes."},
		{Name: "slide-maker", Description: "Build slides from notes and a PDF outline."},
		{Name: "sql-helper", Description: "Write database queries."},
		{Name: "周报", Description: "生成每周工作周报"},
	}
	s := skillmatch.BM25(docs, "pdf notes")
	if s[0] != 1 || !(s[1] > 0 && s[1] < s[0]) || s[2] != 0 {
		t.Fatal("ranking", s)
	}
	if s = skillmatch.BM25(docs, "工作周报"); s[3] != 1 || s[0] != 0 {
		t.Fatal("cjk bigrams", s)
	}
	if s = skillmatch.BM25(docs, "!!"); s[0] != 0 {
		t.Fatal("no terms", s)
	}
}
