package kube

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// What the SDK actually narrates during an uninstall that cannot delete a
// custom resource: a run of ordinary steps, and one line at the end that is the
// whole reason the operation failed.
func narrateAFailedUninstall(l *helmLog) {
	l.record("uninstall: Deleting %s", "custody-core-preprod")
	l.record("Starting delete for %q Deployment", "custody-core")
	l.record("Starting delete for %q Service", "custody-core")
	l.record("Starting delete for %q Redis", "custody-core")
	l.record("uninstall: Failed to delete release: %s", `redis.redis.opstreelabs.in "custody-core" is forbidden: `+
		`User "system:serviceaccount:devops-tools:devops-tools" cannot delete resource "redis" in API group "redis.redis.opstreelabs.in"`)
}

func TestTheReasonAnUninstallFailedSurvives(t *testing.T) {
	l := &helmLog{}
	narrateAFailedUninstall(l)

	got := explain(errors.New("failed to delete release: custody-core-preprod"), l.trouble())
	for _, want := range []string{"forbidden", "redis", "cannot delete"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("the error does not mention %q, which is the whole reason: %v", want, got)
		}
	}
	// And the ordinary narration stays out of it: a message nobody reads to the
	// end says as little as one that never had the reason in it.
	if strings.Contains(got.Error(), "Starting delete") {
		t.Errorf("the whole narration came along: %v", got)
	}
}

func TestNothingToAddLeavesTheErrorAlone(t *testing.T) {
	l := &helmLog{}
	l.record("uninstall: Deleting %s", "custody-core-preprod")

	err := errors.New("release: not found")
	if got := explain(err, l.trouble()); !errors.Is(got, err) {
		t.Errorf("explain() = %v, want the error unchanged", got)
	}
	if explain(nil, l.trouble()) != nil {
		t.Error("explain() invented an error where there was none")
	}
}

// The SDK sometimes returns the detail itself. Saying it twice in one line is
// how an error message stops being read.
func TestTheDetailIsNotRepeated(t *testing.T) {
	l := &helmLog{}
	l.record("Failed to delete: %s", "forbidden")

	err := errors.New(`uninstall failed: Failed to delete: forbidden`)
	got := explain(err, l.trouble())
	if strings.Count(got.Error(), "Failed to delete") != 1 {
		t.Errorf("the detail was added to an error that already carried it: %v", got)
	}
}

// The line worth reading is the last one, so a long run of steps must not push
// it out of the buffer.
func TestALongUninstallKeepsItsLastWords(t *testing.T) {
	l := &helmLog{}
	for i := 0; i < keptHelmLines*3; i++ {
		l.record("Starting delete for %q ConfigMap", fmt.Sprintf("piece-%d", i))
	}
	l.record("uninstall: Failed to delete release: %s", "forbidden")

	if !strings.Contains(l.trouble(), "forbidden") {
		t.Errorf("the failure fell out of the buffer: %q", l.trouble())
	}
	if len(l.lines) > keptHelmLines {
		t.Errorf("kept %d lines, want at most %d", len(l.lines), keptHelmLines)
	}
}
