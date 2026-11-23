// Package skillpkg deterministically inspects an Agent Skill archive: the
// structural gate a publication must pass and the advisory findings frozen
// with it. It calls no model and executes nothing.
package skillpkg

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/lengzhao/taskloom/cap/skills"
	"gopkg.in/yaml.v3"
)

// Limits bound what one archive may expand to.
type Limits struct {
	MaxFiles        int
	MaxUncompressed int64
	MaxSkillMD      int64
}

var DefaultLimits = Limits{MaxFiles: 1000, MaxUncompressed: 50 << 20, MaxSkillMD: 256 << 10}

// Manifest is what a valid archive declares.
type Manifest struct {
	Name, Description, Version string
	Declarations               skills.Declarations
}

var nameRule = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

const (
	maxName        = 64
	maxDescription = 1024
	maxScanned     = 1 << 20 // bytes of one file scanned for risk markers
)

// Inspect checks raw archive bytes. Manifest is meaningful only when no
// returned diagnostic is blocking.
func Inspect(raw []byte, lim Limits) (Manifest, []skills.Diagnostic) {
	var m Manifest
	block := func(code, msg string) (Manifest, []skills.Diagnostic) {
		return m, []skills.Diagnostic{{Code: code, Message: msg, Blocking: true}}
	}
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return block("invalid-archive", "not a readable zip archive")
	}
	if len(zr.File) > lim.MaxFiles {
		return block("too-many-files", fmt.Sprintf("more than %d entries", lim.MaxFiles))
	}
	root := ""
	seen := map[string]bool{}
	var total uint64
	var skillMD *zip.File
	var texts []*zip.File
	for _, f := range zr.File {
		name := f.Name
		if !safePath(name) {
			return block("unsafe-path", "unsafe entry path: "+printable(name))
		}
		if f.Mode()&^0o777 != 0 && !f.FileInfo().IsDir() {
			return block("unsafe-path", "links and special files are not allowed: "+printable(name))
		}
		clean := strings.TrimSuffix(name, "/")
		if seen[clean] {
			return block("duplicate-entry", "duplicate entry: "+printable(name))
		}
		seen[clean] = true
		top, rest, _ := strings.Cut(clean, "/")
		if root == "" {
			root = top
		} else if top != root {
			return block("invalid-layout", "all entries must be under one root directory")
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if rest == "" {
			return block("invalid-layout", "files must be inside the root directory")
		}
		total += f.UncompressedSize64
		if total > uint64(lim.MaxUncompressed) {
			return block("payload-too-large", "archive expands beyond the size limit")
		}
		if rest == "SKILL.md" {
			skillMD = f
		}
		texts = append(texts, f)
	}
	if skillMD == nil {
		return block("missing-skill-md", "root directory has no SKILL.md")
	}
	body, err := readAll(skillMD, lim.MaxSkillMD)
	if err != nil {
		return block("invalid-skill-md", "SKILL.md is unreadable or too large")
	}
	if !utf8.Valid(body) {
		return block("invalid-skill-md-encoding", "SKILL.md must be UTF-8")
	}
	m, diags := parseSkillMD(body)
	for _, d := range diags {
		if d.Blocking {
			return m, diags
		}
	}
	if m.Name != root {
		return block("root-name-mismatch", "root directory must equal frontmatter name")
	}
	diags = append(diags, advisories(m.Declarations)...)
	diags = append(diags, risks(texts)...)
	return m, diags
}

type frontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Metadata    struct {
		Skilla struct {
			Version      string `yaml:"version"`
			RequiredApps []struct {
				Key string `yaml:"key"`
			} `yaml:"required_apps"`
			EgressManifest struct {
				Hosts []string `yaml:"hosts"`
			} `yaml:"egress_manifest"`
			Capability struct {
				WhenToUse       []any `yaml:"when_to_use"`
				NotFor          []any `yaml:"not_for"`
				SuccessCriteria []any `yaml:"success_criteria"`
				Examples        []any `yaml:"examples"`
			} `yaml:"capability"`
		} `yaml:"skilla"`
	} `yaml:"metadata"`
}

func parseSkillMD(body []byte) (Manifest, []skills.Diagnostic) {
	var m Manifest
	bad := func(msg string) (Manifest, []skills.Diagnostic) {
		return m, []skills.Diagnostic{{Code: "invalid-skill-contract", Message: msg, Blocking: true}}
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return bad("SKILL.md must start with YAML frontmatter")
	}
	head, _, ok := strings.Cut(text[4:], "\n---")
	if !ok {
		return bad("SKILL.md frontmatter is not closed")
	}
	var fm frontmatter
	if err := yaml.Unmarshal([]byte(head), &fm); err != nil {
		return bad("frontmatter is not a valid YAML mapping of the expected shape")
	}
	m.Name = strings.TrimSpace(fm.Name)
	m.Description = strings.TrimSpace(fm.Description)
	m.Version = strings.TrimSpace(fm.Metadata.Skilla.Version)
	if len(m.Name) > maxName || !nameRule.MatchString(m.Name) {
		return bad("name must be 1-64 lowercase letters, digits and single hyphens")
	}
	if m.Description == "" || utf8.RuneCountInString(m.Description) > maxDescription {
		return bad("description must be 1-1024 characters")
	}
	if _, ok := ParseVersion(m.Version); !ok {
		return m, []skills.Diagnostic{{Code: "invalid-version", Message: "metadata.skilla.version must be a semantic version", Blocking: true}}
	}
	d := &m.Declarations
	for _, a := range fm.Metadata.Skilla.RequiredApps {
		if k := strings.TrimSpace(a.Key); k != "" {
			d.RequiredApps = append(d.RequiredApps, k)
		}
	}
	for _, h := range fm.Metadata.Skilla.EgressManifest.Hosts {
		if h = strings.TrimSpace(h); h != "" {
			d.EgressHosts = append(d.EgressHosts, h)
		}
	}
	c := fm.Metadata.Skilla.Capability
	d.WhenToUse, d.NotFor, d.SuccessCriteria, d.Examples = texts(c.WhenToUse), texts(c.NotFor), texts(c.SuccessCriteria), texts(c.Examples)
	return m, nil
}

// texts keeps scalar entries; structured examples are counted by their YAML text.
func texts(in []any) []string {
	var out []string
	for _, v := range in {
		var s string
		switch t := v.(type) {
		case string:
			s = t
		default:
			b, _ := yaml.Marshal(t)
			s = string(b)
		}
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func advisories(d skills.Declarations) []skills.Diagnostic {
	var out []skills.Diagnostic
	for _, c := range []struct {
		n    int
		code string
	}{
		{len(d.WhenToUse), "discoverability.when-to-use-missing"},
		{len(d.NotFor), "semantic.not-for-missing"},
		{len(d.SuccessCriteria), "semantic.success-criteria-missing"},
		{len(d.Examples), "discoverability.examples-missing"},
	} {
		if c.n == 0 {
			out = append(out, skills.Diagnostic{Code: c.code, Message: "advisory: declaring this improves discovery and review"})
		}
	}
	return out
}

// Risk markers are advisory evidence for reviewers and hosts; a pattern
// match neither proves nor disproves safety.
var riskMarkers = []struct {
	code string
	re   *regexp.Regexp
}{
	{"risk.network", regexp.MustCompile(`(?i)\b(curl|wget)\s|https?://|requests\.(get|post)|fetch\(`)},
	{"risk.shell", regexp.MustCompile(`subprocess|os\.system|child_process|\bexec\(|bash -c|sh -c`)},
	{"risk.destructive", regexp.MustCompile(`rm -rf|git push --force|DROP TABLE`)},
	{"risk.credentials", regexp.MustCompile(`(?i)api[_-]?key|secret|_token\b|\.ssh/|\.aws/credentials`)},
}

func risks(files []*zip.File) []skills.Diagnostic {
	found := map[string]string{}
	for _, f := range files {
		b, err := readAll(f, maxScanned)
		if err != nil || !utf8.Valid(b) {
			continue // binary or oversized: not scanned
		}
		for _, r := range riskMarkers {
			if _, ok := found[r.code]; !ok && r.re.Match(b) {
				found[r.code] = f.Name
			}
		}
	}
	var out []skills.Diagnostic
	for _, r := range riskMarkers {
		if file, ok := found[r.code]; ok {
			out = append(out, skills.Diagnostic{Code: r.code, Message: "advisory: marker found in " + printable(file)})
		}
	}
	return out
}

func safePath(name string) bool {
	if name == "" || len(name) > 512 || strings.ContainsAny(name, "\\\x00:") || strings.HasPrefix(name, "/") || !utf8.ValidString(name) {
		return false
	}
	for _, seg := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if seg == "" || seg == "." || seg == ".." {
			return false
		}
	}
	return path.Clean(name) == strings.TrimSuffix(name, "/")
}

func readAll(f *zip.File, max int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(max) {
		return nil, fmt.Errorf("too large")
	}
	r, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	b, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil || int64(len(b)) > max {
		return nil, fmt.Errorf("unreadable")
	}
	return b, nil
}

func printable(s string) string {
	if len(s) > 120 {
		s = s[:120]
	}
	return strings.ToValidUTF8(strings.Map(func(r rune) rune {
		if r < 0x20 {
			return '?'
		}
		return r
	}, s), "?")
}
