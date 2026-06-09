package version

import (
	"testing"
	"testing/quick"
)

// TestStringDefault asserts String() returns the unstamped default identity.
func TestStringDefault(t *testing.T) {
	if got := String(); got != "build (unknown)" {
		t.Errorf("String() = %q, want %q", got, "build (unknown)")
	}
}

// TestStringFormatProperty verifies Property 1: for any pair of strings v and
// c, String() returns exactly v + " (" + c + ")".
//
// Validates: Requirements 1.3, 5.4
func TestStringFormatProperty(t *testing.T) {
	origVersion, origCommit := Version, GitCommit
	defer func() {
		Version, GitCommit = origVersion, origCommit
	}()

	f := func(v, c string) bool {
		Version, GitCommit = v, c
		return String() == v+" ("+c+")"
	}
	if err := quick.Check(f, nil); err != nil {
		t.Error(err)
	}
}
