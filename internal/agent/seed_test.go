package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSeed_EmptyDst_Copies(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	mustWrite(t, filepath.Join(src, "agents", "brief.md"), "---\nname: brief\n---\nbody")
	mustWrite(t, filepath.Join(src, "agents", "rude.md"), "---\nname: rude\n---\nbody")
	mustWrite(t, filepath.Join(src, "commands", "review-pr.md"), "---\ndescription: x\n---\nbody")
	mustWrite(t, filepath.Join(src, "jobs", "sysadmin.md"), "---\nname: sysadmin\n---\nbody")

	results, err := Seed(src, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("want 3 results, got %d", len(results))
	}

	byDir := map[string]SeedResult{}
	for _, r := range results {
		byDir[r.Dir] = r
	}
	if byDir["agents"].Copied != 2 {
		t.Errorf("agents copied: got %d, want 2", byDir["agents"].Copied)
	}
	if byDir["commands"].Copied != 1 {
		t.Errorf("commands copied: got %d, want 1", byDir["commands"].Copied)
	}
	if byDir["jobs"].Copied != 1 {
		t.Errorf("jobs copied: got %d, want 1", byDir["jobs"].Copied)
	}
	assertFile(t, filepath.Join(dst, "agents", "brief.md"))
	assertFile(t, filepath.Join(dst, "commands", "review-pr.md"))
}

func TestSeed_ExistingDst_Skipped(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	mustWrite(t, filepath.Join(src, "agents", "brief.md"), "should not be copied")
	mustWrite(t, filepath.Join(dst, "agents", "user-custom.md"), "user's own agent")

	results, err := Seed(src, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	var agentsRow SeedResult
	for _, r := range results {
		if r.Dir == "agents" {
			agentsRow = r
		}
	}
	if !agentsRow.Skipped {
		t.Error("agents should be skipped because user content exists")
	}
	if agentsRow.Copied != 0 {
		t.Errorf("copied: got %d, want 0", agentsRow.Copied)
	}

	// user's file is untouched, src file did NOT land in dst
	assertFile(t, filepath.Join(dst, "agents", "user-custom.md"))
	if _, err := os.Stat(filepath.Join(dst, "agents", "brief.md")); !os.IsNotExist(err) {
		t.Error("src file should not have been copied into non-empty dst")
	}
}

func TestSeed_MissingSrc_Noop(t *testing.T) {
	dst := t.TempDir()
	results, err := Seed("/no/such/path", dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("want nil, got %+v", results)
	}
}

func TestSeed_EmptySrc_Noop(t *testing.T) {
	results, err := Seed("", t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if results != nil {
		t.Errorf("want nil, got %+v", results)
	}
}

func TestSeed_OnlySomeSubdirsPresent(t *testing.T) {
	src := t.TempDir()
	dst := t.TempDir()

	// Only agents exists in src; commands + jobs missing → both should show
	// Copied=0, Skipped=false (source-missing is not the same as dst-nonempty).
	mustWrite(t, filepath.Join(src, "agents", "brief.md"), "body")

	results, err := Seed(src, dst, nil)
	if err != nil {
		t.Fatal(err)
	}
	byDir := map[string]SeedResult{}
	for _, r := range results {
		byDir[r.Dir] = r
	}
	if byDir["agents"].Copied != 1 {
		t.Errorf("agents copied: got %d, want 1", byDir["agents"].Copied)
	}
	if byDir["commands"].Copied != 0 || byDir["commands"].Skipped {
		t.Errorf("commands should be 0/unskipped, got %+v", byDir["commands"])
	}
}

func assertFile(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected file at %s: %v", path, err)
	}
}
