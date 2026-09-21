package esxi

import "testing"

// A long clone polls the same command thousands of times; the log must keep
// what came before it.
func TestAuditFoldsRepeatedCommands(t *testing.T) {
	a := &Audit{}
	a.Add(Event{Category: "preflight", Command: "check"})
	for i := 0; i < 1000; i++ {
		a.Add(Event{Category: "poll", Command: "status", DurationMS: int64(i)})
	}
	a.Add(Event{Category: "poll", Command: "status", ExitCode: 1})
	events := a.Events()
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if events[0].Category != "preflight" || events[0].Runs != 1 {
		t.Fatalf("the earlier command was lost or miscounted: %+v", events[0])
	}
	if events[1].Runs != 1000 || events[1].DurationMS != 999 {
		t.Fatalf("repeats were not folded into the latest run: %+v", events[1])
	}
	if events[2].ExitCode != 1 || events[2].Runs != 1 {
		t.Fatalf("a different result must start a new event: %+v", events[2])
	}
}
