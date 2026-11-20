package attachment

import (
	"testing"

	"github.com/lengzhao/taskloom/cap/commit"
)

func TestValidAndFileName(t *testing.T) {
	id := "0123456789abcdef0123456789abcdef"
	if Valid([]string{id}, []commit.Link{{URL: "https://example.com/a?b=1", Title: "设计"}}) != nil {
		t.Fatal("valid rejected")
	}
	for _, links := range [][]commit.Link{
		{{URL: "javascript:alert(1)"}}, {{URL: "/relative"}}, {{URL: "https://user:pw@example.com"}}, {{URL: "https://example.com", Title: "a\nb"}},
	} {
		if Valid(nil, links) == nil {
			t.Fatal("bad link accepted", links)
		}
	}
	if Valid([]string{"x"}, nil) == nil || Valid([]string{id, id}, nil) == nil {
		t.Fatal("bad file ids accepted")
	}
	if n, ok := FileName(`C:\\docs\\报告.md`); !ok || n != "报告.md" {
		t.Fatal("name", n, ok)
	}
	for _, bad := range []string{"", "..", "dir/", "a\x00b"} {
		if _, ok := FileName(bad); ok {
			t.Fatal("bad name accepted", bad)
		}
	}
}
