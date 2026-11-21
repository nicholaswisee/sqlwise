package jobs

import (
	"strings"
	"testing"
)

func TestOutcomeConstructorsKeepTransportAndEffectApart(t *testing.T) {
	cases := []struct {
		name  string
		got   Outcome
		want  Status
		eff   Effect
		retry bool
	}{
		{"success", Success(), Succeeded, EffectCommitted, false},
		{"failure", Failure("x"), Failed, EffectNone, false},
		{"transient", Transient("x"), Failed, EffectNone, true},
		{"unknown", Unknown("x"), Blocked, EffectUnknown, false},
		{"partial", Partial("x"), Blocked, EffectPartial, false},
	}
	for _, c := range cases {
		if c.got.Status != c.want || c.got.Effect != c.eff || c.got.Retryable != c.retry {
			t.Errorf("%s = %+v", c.name, c.got)
		}
	}
}

func TestTerminalAndRecoveryEligibility(t *testing.T) {
	cases := []struct {
		job      Job
		terminal bool
		recovery int
	}{
		{Job{Status: Queued}, false, 0},
		{Job{Status: Running}, false, 0},
		{Job{Status: Succeeded, Effect: EffectCommitted}, true, 0},
		{Job{Status: Failed}, true, 0},
		// Unknown effects are reconciled by the worker, not by a reviewer.
		{Job{Status: Blocked, Effect: EffectUnknown}, false, 0},
		{Job{Status: Blocked, Effect: EffectPartial}, true, 2},
		{Job{Status: Blocked, Effect: EffectPartial, ResolvedBy: "r"}, true, 0},
	}
	for _, c := range cases {
		if c.job.Terminal() != c.terminal || len(c.job.Recovery()) != c.recovery {
			t.Errorf("%+v: terminal %v recovery %v", c.job, c.job.Terminal(), c.job.Recovery())
		}
	}
}

func TestMetricLabelsAreBounded(t *testing.T) {
	for _, op := range []string{"up", "rollback", "forward", "resume", "compensate"} {
		if operationLabel(op) != op {
			t.Errorf("operation %q relabelled", op)
		}
	}
	if operationLabel("job-1234; DROP") != "other" {
		t.Fatal("unbounded operation leaked into a label")
	}
}

func TestClipKeepsMessagesWithinTheColumn(t *testing.T) {
	long := strings.Repeat("é", 2500)
	if got := []rune(clip(long)); len(got) != 2000 {
		t.Fatalf("clipped to %d runes", len(got))
	}
	if clip("short") != "short" {
		t.Fatal("short message changed")
	}
}
