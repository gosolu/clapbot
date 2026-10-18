package cli

import (
	"os"
	"testing"
)

func TestIsTTY(t *testing.T) {
	// stdin/stdout in testing environment might be a pipe or terminal
	_ = IsTTY()

	// An invalid file descriptor should definitely not be a terminal
	if isTerminal(^uintptr(0)) {
		t.Fatalf("expected invalid fd to not be a terminal")
	}

	// A regular file should not be a terminal
	tmp, err := os.CreateTemp(t.TempDir(), "test_not_tty")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	defer func() { _ = tmp.Close() }()

	if isTerminal(tmp.Fd()) {
		t.Fatalf("expected regular file to not be a terminal")
	}
}
