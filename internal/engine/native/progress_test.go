package native

import "testing"

// The same question answered the same way is not information, however many
// times it is asked.
func TestIdenticalCallsAreCountedAsRepeats(t *testing.T) {
	p := newProgress()
	for i := 1; i <= 3; i++ {
		v, times := p.observe("read_file", `{"path":"a.go"}`, "package a")
		if i == 1 && v != learned {
			t.Fatalf("the first call was not counted as new")
		}
		if i > 1 && v != repeated {
			t.Errorf("call %d was not counted as a repeat", i)
		}
		if times != i {
			t.Errorf("call %d reported %d occurrences", i, times)
		}
	}
	stuck, why := p.stuck()
	if !stuck {
		t.Fatal("three identical calls with identical answers did not end the loop")
	}
	if want := "read_file"; !contains(why, want) {
		t.Errorf("the reason must name the call, got %q", why)
	}
}

// Arguments that differ only in key order or spacing are the same call. A
// model that reformats its JSON is not making progress.
func TestArgumentOrderDoesNotDisguiseARepeat(t *testing.T) {
	p := newProgress()
	p.observe("edit_file", `{"path":"a.go","old":"x","new":"y"}`, "ok")
	v, times := p.observe("edit_file", `{"new":"y","old":"x","path":"a.go"}`, "ok")
	if v != repeated || times != 2 {
		t.Errorf("reordered arguments were treated as a new call (v=%v times=%d)", v, times)
	}
}

// The same call answered differently is real information: the file changed, or
// the build now fails for another reason. Counting it as a repeat would punish
// a model for checking its own work.
func TestTheSameCallWithANewAnswerIsProgress(t *testing.T) {
	p := newProgress()
	p.observe("run_verification", `{}`, "1 failure")
	v, _ := p.observe("run_verification", `{}`, "passing")
	if v != learned {
		t.Error("a changed answer was not counted as progress")
	}
	if stuck, _ := p.stuck(); stuck {
		t.Error("a loop that is learning was stopped")
	}
}

// A stale step is survivable; three in a row is a strategy that is not working.
func TestConsecutiveStaleStepsEndTheLoop(t *testing.T) {
	p := newProgress()
	p.observe("list_files", `{"path":"."}`, "a.go")
	for range staleLimit - 1 {
		p.endOfStep(false)
		if stuck, _ := p.stuck(); stuck {
			t.Fatal("the loop was stopped before the stale limit")
		}
	}
	p.endOfStep(false)
	stuck, why := p.stuck()
	if !stuck {
		t.Fatal("consecutive stale steps did not end the loop")
	}
	if !contains(why, "learned nothing new") {
		t.Errorf("the reason should say what happened, got %q", why)
	}
}

// One good step clears the stale run: a model that reads two files it has
// already seen and then edits something is working, not stuck.
func TestProgressResetsTheStaleCount(t *testing.T) {
	p := newProgress()
	p.endOfStep(false)
	p.endOfStep(false)
	p.endOfStep(true)
	p.endOfStep(false)
	if stuck, why := p.stuck(); stuck {
		t.Errorf("a loop that made progress was stopped: %s", why)
	}
}

// The note is the supervisor speaking and has to be actionable, not a counter.
func TestTheRepeatNoteTellsTheModelWhatToDo(t *testing.T) {
	note := repeatNote("search_code", 2)
	for _, want := range []string{"search_code", "[supervisor]", "already have"} {
		if !contains(note, want) {
			t.Errorf("the note does not contain %q: %s", want, note)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}
