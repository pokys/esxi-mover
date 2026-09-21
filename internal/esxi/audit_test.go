package esxi

import (
	"testing"
	"time"
)

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

func TestAuditKeepsHistoryAcrossAlternatingPolls(t *testing.T) {
	a := &Audit{}
	a.Add(Event{Category: "create-snapshot", Command: "snapshot"})
	for i := 0; i < 1000; i++ {
		a.Add(Event{Category: "poll-detached-clone", Command: "status", Time: time.Unix(int64(i*2), 0)})
		a.Add(Event{Category: "datastores", Command: "space", Time: time.Unix(int64(i*2+1), 0)})
	}
	events := a.Events()
	if len(events) != 3 || events[0].Category != "create-snapshot" || events[1].Runs != 1000 || events[2].Runs != 1000 {
		t.Fatalf("polls displaced the operation history: %+v", events)
	}
	if events[1].Time.After(events[2].Time) {
		t.Fatal("folded polls are out of time order")
	}
	for _, boundary := range []Event{
		{Category: "stop-clone", Command: "stop"},
		{Category: "datastores", Command: "space", Error: "connection lost", ExitCode: -1},
	} {
		a.Add(boundary)
		a.Add(Event{Category: "poll-detached-clone", Command: "status"})
		a.Add(Event{Category: "datastores", Command: "space"})
		events = a.Events()
		if events[len(events)-3].Category != boundary.Category || events[len(events)-2].Runs != 1 || events[len(events)-1].Runs != 1 {
			t.Fatalf("polls were folded across an operation or error: %+v", events)
		}
	}
}
