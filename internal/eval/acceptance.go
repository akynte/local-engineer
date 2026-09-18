package eval

// Acceptance metrics answer a question separate from "did it work": when the
// system said it was finished, was it right?
//
// A tool that fails loudly is usable. A tool that fails while reporting success
// is worse than no tool, because the failure is now in the user's tree with a
// green tick next to it. That is why this is a first-class metric rather than
// something a reader derives.

// Confusion is the four-way breakdown of claim against ground truth.
//
// The names are from the system's point of view: it "accepts" a run when it
// reaches its own completion state. Ground truth is the hidden acceptance
// command, which the system never sees.
type Confusion struct {
	// TrueAccept: claimed done, and was done.
	TrueAccept int `json:"true_accept"`
	// FalseAccept: claimed done, was not. The damaging one.
	FalseAccept int `json:"false_accept"`
	// TrueReject: did not claim done, and was not done.
	TrueReject int `json:"true_reject"`
	// FalseReject: did not claim done, but the work was right. Wasteful
	// rather than dangerous — the contract was too strict somewhere.
	FalseReject int `json:"false_reject"`
}

// Total is every run counted.
func (c Confusion) Total() int {
	return c.TrueAccept + c.FalseAccept + c.TrueReject + c.FalseReject
}

// Claims is how many runs the system declared complete. This is the
// denominator that answers "when it said yes, how often was it wrong".
func (c Confusion) Claims() int { return c.TrueAccept + c.FalseAccept }

// FalseAcceptanceRate is false accepts over runs where the system declared
// completion.
//
// This denominator is the one that matches the question a user asks, and it is
// named in full rather than abbreviated because "FAR" has been used for both
// denominators in different papers and the ambiguity hides which is meant.
// Rate carries the count and the total, so the denominator travels with the
// number.
func (c Confusion) FalseAcceptanceRate() Rate {
	return NewRate(c.FalseAccept, c.Claims())
}

// FalseAcceptanceShare is false accepts over all evidence runs. Reported beside
// the rate because a system that rarely claims completion can have a terrible
// rate and still put few bad changes in front of a user, and the two numbers
// together say that where either alone would mislead.
func (c Confusion) FalseAcceptanceShare() Rate {
	return NewRate(c.FalseAccept, c.Total())
}

// FalseRejectionRate is missed successes over runs the system did not accept.
func (c Confusion) FalseRejectionRate() Rate {
	return NewRate(c.FalseReject, c.TrueReject+c.FalseReject)
}

// ConfusionOf builds the breakdown for one arm over the runs that count as
// evidence. Environmental failures, timeouts and invalid runs are excluded
// here and reported separately, because a crashed harness is not a false
// claim by the system.
func ConfusionOf(outcomes []Outcome, arm string) Confusion {
	var c Confusion
	for _, o := range outcomes {
		if o.Arm != arm || !o.EvidenceRun() {
			continue
		}
		switch {
		case o.Claimed && o.Solved:
			c.TrueAccept++
		case o.Claimed && !o.Solved:
			c.FalseAccept++
		case !o.Claimed && o.Solved:
			c.FalseReject++
		default:
			c.TrueReject++
		}
	}
	return c
}

// StatusCounts reports the shape of a run set: how much of it was evidence and
// how much was the machine failing. A report that shows only the rate invites
// the reader to assume the denominator was the whole set.
func StatusCounts(outcomes []Outcome, arm string) map[Status]int {
	counts := map[Status]int{}
	for _, o := range outcomes {
		if arm != "" && o.Arm != arm {
			continue
		}
		counts[o.Classified()]++
	}
	return counts
}
