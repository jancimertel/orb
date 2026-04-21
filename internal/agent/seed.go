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
// (skills, agents, jobs, …) should be added here and to its own loader
// method.
var seedSubdirs = []string{"agents", "commands", "jobs"}

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
		res := SeedResult{Dir: sub}
		srcSub := filepath.Join(srcRoot, sub)
		dstSub := filepath.Join(dstRoot, sub)

		if _, err := os.Stat(srcSub); err != nil {
			out = append(out, res)
			continue
		}
		hasContent, err := dirHasMarkdown(dstSub)
		if err != nil {
			return out, fmt.Errorf("seed: inspect dst %s: %w", dstSub, err)
		}
		if hasContent {
			res.Skipped = true
			out = append(out, res)
			if logger != nil {
				logger.Debug("seed skipped — dst not empty",
					"dir", sub, "dst", dstSub)
			}
			continue
		}
		if err := os.MkdirAll(dstSub, 0o755); err != nil {
			return out, fmt.Errorf("seed: mkdir %s: %w", dstSub, err)
		}
		copied, err := copyMarkdown(srcSub, dstSub)
		if err != nil {
			return out, fmt.Errorf("seed: copy %s → %s: %w", srcSub, dstSub, err)
		}
		res.Copied = copied
		out = append(out, res)
		if logger != nil && copied > 0 {
			logger.Info("seeded .claude subdir",
				"dir", sub, "copied", copied, "dst", dstSub)
		}
	}
	return out, nil
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
