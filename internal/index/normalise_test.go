package index

import (
	"testing"
	"time"
)

// A database written before the two writers agreed on a unit still holds
// milliseconds, and no migration rewrites them because the two units are
// indistinguishable by type. The reader has to cope, or every consumer reports
// a date tens of thousands of years in the future — which is how this was
// found, in a status tool that printed one.
func TestALegacyMillisecondTimestampIsReadAsATime(t *testing.T) {
	const millis = int64(1789632657782) // taken from a real pre-fix database
	got := normaliseIndexedAt(millis)
	age := time.Since(time.Unix(got, 0))
	if age < 0 {
		t.Errorf("a legacy millisecond row still reads %s in the future", -age)
	}
	if age > 100*365*24*time.Hour {
		t.Errorf("a legacy millisecond row reads as %s old", age)
	}
}

// A seconds timestamp must pass through untouched, or the fix would divide
// every current row by a thousand and report 1970.
func TestASecondsTimestampIsNotRescaled(t *testing.T) {
	now := time.Now().Unix()
	if got := normaliseIndexedAt(now); got != now {
		t.Errorf("normaliseIndexedAt(%d) = %d; a current timestamp was rescaled", now, got)
	}
}
