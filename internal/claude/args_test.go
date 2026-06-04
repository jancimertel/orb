package claude

import "testing"

// hasFlagValue reports whether args contains flag immediately followed by value.
func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// hasFlag reports whether args contains flag at all.
func hasFlag(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func TestBuildArgs_EffortSet(t *testing.T) {
	args, err := buildArgs(SpawnOpts{Model: "claude-opus-4-8", Effort: "xhigh"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if !hasFlagValue(args, "--effort", "xhigh") {
		t.Errorf("expected --effort xhigh in %v", args)
	}
	if !hasFlagValue(args, "--model", "claude-opus-4-8") {
		t.Errorf("expected --model claude-opus-4-8 in %v", args)
	}
}

func TestBuildArgs_EffortOmittedWhenEmpty(t *testing.T) {
	args, err := buildArgs(SpawnOpts{Model: "claude-opus-4-8"})
	if err != nil {
		t.Fatalf("buildArgs: %v", err)
	}
	if hasFlag(args, "--effort") {
		t.Errorf("did not expect --effort in %v", args)
	}
}
