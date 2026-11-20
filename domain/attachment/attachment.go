// Package attachment holds the plain rules for files and links on a commit.
package attachment

import (
	"net/url"
	"strings"
	"unicode"

	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
)

const (
	MaxFiles    = 20
	MaxLinks    = 20
	MaxURL      = 2048
	MaxTitle    = 200
	MaxFileName = 255
)

// Valid checks the shape of a commit's references: at most MaxFiles distinct
// file IDs and MaxLinks absolute http(s) links. Whether each file exists on
// the task is checked when the commit is appended.
func Valid(files []string, links []commit.Link) error {
	if len(files) > MaxFiles || len(links) > MaxLinks {
		return fault.ErrInput
	}
	seen := map[string]bool{}
	for _, id := range files {
		if !FileID(id) || seen[id] {
			return fault.ErrInput
		}
		seen[id] = true
	}
	for _, l := range links {
		if len(l.URL) > MaxURL || len(l.Title) > MaxTitle || !printable(l.Title) || !printable(l.URL) {
			return fault.ErrInput
		}
		u, e := url.Parse(l.URL)
		if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
			return fault.ErrInput
		}
	}
	return nil
}

// Empty reports whether a commit carries neither text nor references.
func Empty(body string, files []string, links []commit.Link) bool {
	return strings.TrimSpace(body) == "" && len(files) == 0 && len(links) == 0
}

// Same reports whether two commits carry the same text and references.
func Same(a commit.Commit, body string, files []string, links []commit.Link) bool {
	if a.Body != body || len(a.Files) != len(files) || len(a.Links) != len(links) {
		return false
	}
	for i := range files {
		if a.Files[i] != files[i] {
			return false
		}
	}
	for i := range links {
		if a.Links[i] != links[i] {
			return false
		}
	}
	return true
}

// FileID reports whether id has the shape of a generated file ID: 32 lower hex digits.
func FileID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, r := range id {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// FileName cleans an uploaded name to its last path element; it reports false
// for a name that is empty, too long or contains control characters.
func FileName(name string) (string, bool) {
	if i := strings.LastIndexAny(name, `/\`); i >= 0 {
		name = name[i+1:]
	}
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || len(name) > MaxFileName || !printable(name) {
		return "", false
	}
	return name, true
}

func printable(s string) bool {
	for _, r := range s {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}
