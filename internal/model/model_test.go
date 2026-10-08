package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestRunLabels(t *testing.T) {
	for _, tc := range []struct {
		kind    Kind
		outcome OutcomeKind
		stop    bool
		want    string
	}{
		{Task, Success, false, "completed"}, {Task, NonzeroExit, false, "failed"},
		{Task, Signaled, false, "failed"}, {Service, Success, false, "exited"},
		{Service, NonzeroExit, false, "exited"}, {Task, Signaled, true, "stopped"},
		{Service, LaunchFailed, false, "launch failed"},
	} {
		r := Run{Definition: Definition{Kind: tc.kind}, Lifecycle: Exited, Outcome: &Outcome{Kind: tc.outcome}, StopRequested: tc.stop}
		if r.Label() != tc.want {
			t.Fatalf("%+v: %s", tc, r.Label())
		}
	}
	if (Run{Lifecycle: Stopping}).Label() != "stopping" {
		t.Fatal("active state changed")
	}
}

func TestSnapshotsAndEnvironmentRedaction(t *testing.T) {
	code := 7
	now := time.Now()
	r := Run{Definition: Definition{Env: map[string]string{"SECRET": "never-persist-me"}}, Outcome: &Outcome{ExitCode: &code}, EndedAt: &now}
	copy := r.Clone()
	copy.Definition.Env["SECRET"] = "changed"
	*copy.Outcome.ExitCode = 0
	*copy.EndedAt = time.Time{}
	if r.Definition.Env["SECRET"] != "never-persist-me" || *r.Outcome.ExitCode != 7 || r.EndedAt.IsZero() {
		t.Fatal("snapshot aliases original")
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), "never-persist-me") {
		t.Fatal("environment leaked into metadata")
	}
}
