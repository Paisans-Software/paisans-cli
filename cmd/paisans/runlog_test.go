package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/paisans-software/paisans-cli/internal/runlog"
)

func TestOnlyAnExecutedApplyWritesARunLog(t *testing.T) {
	base := t.TempDir()
	t.Setenv("PAISANS_DATA_DIR", base)

	openRunLog("deployment apply", []string{"--config", "paisans-stack.yaml"})()
	if _, err := os.Stat(runlog.Dir(base)); !os.IsNotExist(err) {
		t.Fatalf("a dry run left %s behind (%v)", runlog.Dir(base), err)
	}

	openRunLog("deployment apply", []string{"--execute"})()
	logs, _ := filepath.Glob(filepath.Join(runlog.Dir(base), "*-deployment-apply.log"))
	if len(logs) != 1 {
		t.Fatalf("an executed apply wrote %d logs, want 1", len(logs))
	}
}
