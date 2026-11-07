package launch_test

import "testing"

func TestAdoptEndsNeverStartedRun(t *testing.T) {
	f := newFx(t)
	res := f.spawn("z") // run created, but the hub "died" before Start
	f.restart()
	f.launch.Adopt()
	exits := f.of(res.RunID, "exit")
	if len(exits) != 1 || exits[0].Payload["detail"] != "never started (hub stopped during spawn)" {
		t.Fatalf("exits: %+v", exits)
	}
	if !f.st.Get(res.RunID).Ended() {
		t.Fatal("run not ended")
	}
	f.launch.Adopt() // idempotent
	if n := len(f.of(res.RunID, "exit")); n != 1 {
		t.Fatalf("exit recorded %d times", n)
	}
}

func TestAdoptLeavesRunsOfThisProcess(t *testing.T) {
	f := newFx(t)
	res := f.spawn("z") // created after the launcher: a spawn in progress
	f.launch.Adopt()
	if n := len(f.of(res.RunID, "exit")); n != 0 {
		t.Fatalf("in-flight spawn ended: %d", n)
	}
}
