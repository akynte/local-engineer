// The docs site of design v3 §1.1 ("a `docs/` site built from Markdown is
// preferred over a wiki") and §15 ("Docs site (Diátaxis)").
//
// mkdocs.yml is a *view* of docs/, never a second source — every page in it is
// a file that also reads correctly on GitHub, because the repository is where
// people actually find documentation. A view drifts silently: a page added to
// docs/ but not the nav is invisible on the site, and a nav entry whose file
// moved breaks the build at deploy time rather than here.
//
// So this test is the thing that keeps the two in step. It has no build tag,
// because it needs no image and no network: it reads two lists and compares
// them.
package docs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var navEntry = regexp.MustCompile(`(?m):\s+([A-Za-z0-9_./-]+\.md)\s*$`)

func TestTheSiteNavAndTheDocsDirectoryAgree(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "mkdocs.yml"))
	if err != nil {
		t.Fatalf("mkdocs.yml is the docs site of §1.1 and §15; without it there is no site: %v", err)
	}

	inNav := map[string]bool{}
	for _, m := range navEntry.FindAllStringSubmatch(string(body), -1) {
		inNav[m[1]] = true
	}
	if len(inNav) == 0 {
		t.Fatal("mkdocs.yml lists no pages")
	}

	onDisk := map[string]bool{}
	err = filepath.WalkDir(".", func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(p, ".md") {
			return nil
		}
		onDisk[filepath.ToSlash(p)] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	var missing, orphaned []string
	for p := range inNav {
		if !onDisk[p] {
			missing = append(missing, p)
		}
	}
	for p := range onDisk {
		if !inNav[p] {
			orphaned = append(orphaned, p)
		}
	}
	sort.Strings(missing)
	sort.Strings(orphaned)

	for _, p := range missing {
		t.Errorf("mkdocs.yml lists %s, which does not exist; the site build would fail", p)
	}
	for _, p := range orphaned {
		// Not a warning: a page nobody can navigate to is a page nobody reads,
		// and the usual cause is that it was added and the nav forgotten.
		t.Errorf("docs/%s is not in mkdocs.yml, so it is invisible on the site", p)
	}
}

func TestEveryDiataxisSectionExists(t *testing.T) {
	// §1.1 names the split, and §15 requires the site to follow it. A section
	// that quietly emptied would be a category of documentation this project
	// stopped writing.
	for _, dir := range []string{"tutorials", "how-to", "reference", "explanation"} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Errorf("the Diátaxis section %q is missing: %v", dir, err)
			continue
		}
		var pages int
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".md") {
				pages++
			}
		}
		if pages == 0 {
			t.Errorf("the Diátaxis section %q has no pages", dir)
		}
	}
}
