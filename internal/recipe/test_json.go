package recipe

import (
	"bufio"
	"encoding/json"
	"strings"
)

// GoTestJSON retains individual test outcomes so a newly broken test is a
// regression even when another test was already failing in the baseline.
func GoTestJSON(exit int, stdout, stderr string) (Status, Summary) {
	var text strings.Builder
	tests := map[string]Status{}
	scan := bufio.NewScanner(strings.NewReader(stdout))
	scan.Buffer(make([]byte, 4096), 8<<20)
	for scan.Scan() {
		var event struct{ Action, Package, Test, Output string }
		if json.Unmarshal(scan.Bytes(), &event) != nil {
			text.WriteString(scan.Text() + "\n")
			continue
		}
		text.WriteString(event.Output)
		if event.Test != "" {
			switch event.Action {
			case "pass":
				tests[event.Package+"/"+event.Test] = Pass
			case "fail":
				tests[event.Package+"/"+event.Test] = Fail
			case "skip":
				tests[event.Package+"/"+event.Test] = Skipped
			}
		}
	}
	status, summary := GoTest(exit, text.String(), stderr)
	summary.Tests = tests
	if scan.Err() != nil {
		status = Error
		summary.Headline = "test JSON exceeded capture limits"
	}
	return status, summary
}
