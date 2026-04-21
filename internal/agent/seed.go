package agent

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// seedSubdirs is the fixed set of .claude subdirectories the seeder
// manages. Keep in lockstep with the loader: any new kind of content file
// (agents, commands, skills, jobs, …) should be added here and to its own
// loader method.
//
// Note: `skills/` holds native Claude Code skill packs (directories with
// SKILL.md inside), not top-level .md files — they're seeded by a
// separate code path (copyMarkdownTree) since copyMarkdown only walks
// top-level .md entries.
var seedSubdirs = []string{"agents", "commands", "jobs"}
var seedTreeSubdirs = []string{"skills"}

// SeedResult reports what Seed did per subdirectory, for structured logging.
type SeedResult struct {
	Dir     string // "agents" | "commands" | "jobs"
	Copied  int    // count of .md files copied from srcDir into dstDir
	Skipped bool   // true if the dst already contained user content
}

// Seed copies example .claude/{agents,commands,jobs} files from srcRoot into
// dstRoot, one subdirectory at a time. For each subdir:
//
//   - If dst already has any .md file, Seed leaves it alone (the operator has
//     customizations we must not clobber).
//   - Otherwise, every .md under src/<subdir> is copied into dst/<subdir>.
//
// Missing src or src-subdir is not an error — it just yields Skipped=false
// and Copied=0 for that row. Callers typically receive srcRoot from the
// EXAMPLES_DIR env var, which is empty for local dev.
//
// srcRoot should already contain the `.claude` prefix — i.e. you pass
// `/opt/bot/examples/.claude`, not `/opt/bot/examples`. dstRoot should be
// the live `.claude` tree on the persistent volume, e.g. `/data/.claude`.
func Seed(srcRoot, dstRoot string, logger *slog.Logger) ([]SeedResult, error) {
	if srcRoot == "" {
		return nil, nil
	}
	if _, err := os.Stat(srcRoot); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("seed: stat src: %w", err)
	}

	var out []SeedResult
	for _, sub := range seedSubdirs {
		res, err := seedFlatSubdir(srcRoot, dstRoot, sub, logger)
		if err != nil {
			out = append(out, res)
			return out, err
		}
		out = append(out, res)
	}
	for _, sub := range seedTreeSubdirs {
		res, err := seedTreeSubdir(srcRoot, dstRoot, sub, logger)
		if err != nil {
			out = append(out, res)
			return out, err
		}
		out = append(out, res)
	}
	return out, nil
}

// seedFlatSubdir handles `agents/`, `commands/`, `jobs/` — subdirs whose
// content is top-level .md files only.
func seedFlatSubdir(srcRoot, dstRoot, sub string, logger *slog.Logger) (SeedResult, error) {
	res := SeedResult{Dir: sub}
	srcSub := filepath.Join(srcRoot, sub)
	dstSub := filepath.Join(dstRoot, sub)

	if _, err := os.Stat(srcSub); err != nil {
		return res, nil
	}
	hasContent, err := dirHasMarkdown(dstSub)
	if err != nil {
		return res, fmt.Errorf("seed: inspect dst %s: %w", dstSub, err)
	}
	if hasContent {
		res.Skipped = true
		if logger != nil {
			logger.Debug("seed skipped — dst not empty", "dir", sub, "dst", dstSub)
		}
		return res, nil
	}
	if err := os.MkdirAll(dstSub, 0o755); err != nil {
		return res, fmt.Errorf("seed: mkdir %s: %w", dstSub, err)
	}
	copied, err := copyMarkdown(srcSub, dstSub)
	if err != nil {
		return res, fmt.Errorf("seed: copy %s → %s: %w", srcSub, dstSub, err)
	}
	res.Copied = copied
	if logger != nil && copied > 0 {
		logger.Info("seeded .claude subdir", "dir", sub, "copied", copied, "dst", dstSub)
	}
	return res, nil
}

// seedTreeSubdir handles `skills/` — subdirs whose content is itself a
// directory per entry (each holding a SKILL.md plus optional supporting
// files). We copy the whole tree when dst has no existing skill dirs.
func seedTreeSubdir(srcRoot, dstRoot, sub string, logger *slog.Logger) (SeedResult, error) {
	res := SeedResult{Dir: sub}
	srcSub := filepath.Join(srcRoot, sub)
	dstSub := filepath.Join(dstRoot, sub)

	if _, err := os.Stat(srcSub); err != nil {
		return res, nil
	}
	hasContent, err := dirHasChildDir(dstSub)
	if err != nil {
		return res, fmt.Errorf("seed: inspect dst %s: %w", dstSub, err)
	}
	if hasContent {
		res.Skipped = true
		if logger != nil {
			logger.Debug("seed skipped — dst not empty", "dir", sub, "dst", dstSub)
		}
		return res, nil
	}
	if err := os.MkdirAll(dstSub, 0o755); err != nil {
		return res, fmt.Errorf("seed: mkdir %s: %w", dstSub, err)
	}
	copied, err := copyTree(srcSub, dstSub)
	if err != nil {
		return res, fmt.Errorf("seed: copy tree %s → %s: %w", srcSub, dstSub, err)
	}
	res.Copied = copied
	if logger != nil && copied > 0 {
		logger.Info("seeded .claude subdir", "dir", sub, "copied", copied, "dst", dstSub)
	}
	return res, nil
}

// dirHasChildDir reports whether dir contains at least one subdirectory.
// Missing dir → false, not an error.
func dirHasChildDir(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range entries {
		if e.IsDir() {
			return true, nil
		}
	}
	return false, nil
}

// copyTree recursively copies every file (and subdirectory) under src into
// dst. Returns the number of subdirectories directly under src that were
// copied — the unit of "one skill".
func copyTree(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	var n int
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return n, err
			}
			if _, err := copyTreeInner(srcPath, dstPath); err != nil {
				return n, err
			}
			n++
			continue
		}
		// Top-level stray files (e.g. README.md inside skills/) are skipped —
		// skills are organized per directory.
	}
	return n, nil
}

// copyTreeInner walks every file and subdirectory under src → dst.
// Returns the number of files copied (informational).
func copyTreeInner(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	var n int
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := os.MkdirAll(dstPath, 0o755); err != nil {
				return n, err
			}
			sub, err := copyTreeInner(srcPath, dstPath)
			if err != nil {
				return n, err
			}
			n += sub
			continue
		}
		if err := copyFile(srcPath, dstPath); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// dirHasMarkdown reports whether dir contains at least one .md file (top
// level only). Missing dir → false, not an error.
func dirHasMarkdown(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".md" {
			return true, nil
		}
	}
	return false, nil
}

// copyMarkdown copies every top-level *.md file from src into dst and returns
// the number of files copied. Non-.md entries and subdirectories are
// ignored so a top-level README doesn't leak in.
func copyMarkdown(src, dst string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	var n int
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}
