package skillpkg_test

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/lengzhao/taskloom/domain/skillpkg"
)

type entry struct{ name, body string }

func archive(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, e := range entries {
		f, err := w.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte(e.body))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const good = `---
name: pdf-reader
description: Reads PDF files and extracts their text.
metadata:
  skilla:
    version: 1.2.0
    required_apps:
      - key: drive
    egress_manifest:
      hosts: [api.example.com]
    capability:
      when_to_use: [a PDF must be read]
      examples:
        - input: report.pdf
---
# PDF reader
`

func TestValidArchive(t *testing.T) {
	m, ds := skillpkg.Inspect(archive(t, entry{"pdf-reader/SKILL.md", good}, entry{"pdf-reader/scripts/run.sh", "curl https://api.example.com"}), skillpkg.DefaultLimits)
	for _, d := range ds {
		if d.Blocking {
			t.Fatal("blocked", d)
		}
	}
	if m.Name != "pdf-reader" || m.Version != "1.2.0" || m.Declarations.RequiredApps[0] != "drive" || m.Declarations.EgressHosts[0] != "api.example.com" || len(m.Declarations.Examples) != 1 {
		t.Fatalf("manifest %+v", m)
	}
	var got []string
	for _, d := range ds {
		got = append(got, d.Code)
	}
	all := strings.Join(got, ",")
	for _, want := range []string{"semantic.not-for-missing", "semantic.success-criteria-missing", "risk.network"} {
		if !strings.Contains(all, want) {
			t.Errorf("missing advisory %s in %s", want, all)
		}
	}
	if strings.Contains(all, "when-to-use-missing") || strings.Contains(all, "examples-missing") {
		t.Error("declared fields reported missing", all)
	}
}

func TestBlockingFindings(t *testing.T) {
	bad := func(fm string) string { return strings.Replace(good, "name: pdf-reader", fm, 1) }
	cases := map[string][]byte{
		"invalid-archive":           []byte("not a zip"),
		"missing-skill-md":          archive(t, entry{"pdf-reader/README.md", "x"}),
		"unsafe-path":               archive(t, entry{"pdf-reader/../evil", "x"}),
		"invalid-layout":            archive(t, entry{"pdf-reader/SKILL.md", good}, entry{"other/x", "y"}),
		"root-name-mismatch":        archive(t, entry{"reader/SKILL.md", good}),
		"invalid-skill-contract":    archive(t, entry{"Bad_Name/SKILL.md", bad("name: Bad_Name")}),
		"invalid-version":           archive(t, entry{"pdf-reader/SKILL.md", strings.Replace(good, "1.2.0", "v1.2", 1)}),
		"invalid-skill-md-encoding": archive(t, entry{"pdf-reader/SKILL.md", "---\xff"}),
	}
	for want, raw := range cases {
		_, ds := skillpkg.Inspect(raw, skillpkg.DefaultLimits)
		if len(ds) == 0 || !ds[0].Blocking || ds[0].Code != want {
			t.Errorf("%s: got %+v", want, ds)
		}
	}
	lim := skillpkg.DefaultLimits
	lim.MaxUncompressed = 10
	if _, ds := skillpkg.Inspect(archive(t, entry{"pdf-reader/SKILL.md", good}), lim); len(ds) == 0 || ds[0].Code != "payload-too-large" {
		t.Errorf("size limit: %+v", ds)
	}
}

func TestVersionOrder(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.10.0", "2.0.0"}
	for i := 1; i < len(ordered); i++ {
		if !skillpkg.Newer(ordered[i], ordered[i-1]) || skillpkg.Newer(ordered[i-1], ordered[i]) {
			t.Errorf("%s should follow %s", ordered[i], ordered[i-1])
		}
	}
	for _, s := range []string{"1.0", "01.0.0", "1.0.0-01", "v1.0.0", "1.0.0+", ""} {
		if _, ok := skillpkg.ParseVersion(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
	if skillpkg.Newer("1.0.0", "1.0.0+build") {
		t.Error("build metadata must not order")
	}
}
