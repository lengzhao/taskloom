package architecture_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var (
	markdownLink = regexp.MustCompile(`\]\(([^)]+)\)`)
	lineSuffix   = regexp.MustCompile(`:\d+$`)
)

// Repository docs are the agents' rule source: relative links must resolve and
// code fences must close. External URLs and absolute workstation paths are skipped.
func TestDocumentLinksAndFences(t *testing.T) {
	root := filepath.Join("..", "..")
	files := []string{"README.md", "AGENTS.md", "todo.md"}
	for _, pattern := range []string{"docs/*.md", "examples/*.md"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			files = append(files, rel)
		}
	}
	links, problems, err := checkDocs(root, files)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range problems {
		t.Error(p)
	}
	if links == 0 {
		t.Fatal("no document links checked")
	}
}

func TestDocumentCheckRejectsBrokenLinkAndOpenFence(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ok.md"), []byte("[self](ok.md#top) [web](https://x) [line](ok.md:3)\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bad.md"), []byte("[gone](missing.md)\n```sh\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	links, problems, err := checkDocs(root, []string{"ok.md", "bad.md"})
	if err != nil {
		t.Fatal(err)
	}
	if links != 3 || len(problems) != 2 {
		t.Fatalf("links=%d problems=%v", links, problems)
	}
}

func checkDocs(root string, files []string) (int, []string, error) {
	links := 0
	var problems []string
	for _, name := range files {
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			return links, problems, err
		}
		text := string(raw)
		fences := 0
		for line := range strings.SplitSeq(text, "\n") {
			if strings.HasPrefix(line, "```") {
				fences++
			}
		}
		if fences%2 != 0 {
			problems = append(problems, "unclosed code fence in "+name)
		}
		for _, m := range markdownLink.FindAllStringSubmatch(text, -1) {
			target := m[1]
			if strings.Contains(target, "://") || strings.HasPrefix(target, "#") {
				continue
			}
			local := lineSuffix.ReplaceAllString(strings.SplitN(target, "#", 2)[0], "")
			if filepath.IsAbs(local) {
				continue
			}
			links++
			if _, err := os.Stat(filepath.Join(root, filepath.Dir(name), local)); err != nil {
				problems = append(problems, "broken link in "+name+": "+target)
			}
		}
	}
	return links, problems, nil
}
