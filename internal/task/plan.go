package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
)

// Decomposition into bounded child tasks with executable acceptance is the
// first technique in design v3 §10.1, adopted against "long tasks, drift".
//
// The plan is produced by a model but is not trusted by one. A plan that names
// a file outside the repository, declares no scope, or carries no acceptance
// criterion is rejected here — before any of it runs — because those are the
// failures that turn a long task into a long mess.

// Plan is a decomposition of a requirement.
type Plan struct {
	RequirementID string     `json:"requirement_id"`
	Objective     string     `json:"objective"`
	Steps         []PlanStep `json:"steps"`
	// Rationale is the planner's reasoning, kept for the human gate.
	Rationale string `json:"rationale,omitempty"`
	// Impact is what the supervisor computed about the symbols the plan
	// touches. It is attached by the planner, not by the model, so the gate
	// sees the deterministic answer rather than the model's summary of it.
	Impact *graph.Impact `json:"impact,omitempty"`
}

// PlanStep is one bounded child task.
type PlanStep struct {
	Title string `json:"title"`
	// Scope lists the path prefixes this step may change. It is required: a
	// step with unbounded scope cannot have an out-of-scope violation, which
	// removes the only automatic check on a step doing too much.
	Scope []string `json:"scope"`
	// Verification is the level this step's completion demands.
	Verification recipe.Level `json:"verification"`
	// Acceptance is what makes the step done beyond the recipes.
	Acceptance []Check `json:"acceptance,omitempty"`
	// DependsOn indexes earlier steps that must finish first.
	DependsOn []int `json:"depends_on,omitempty"`
}

// planSchema constrains the model's output. Structured output is used rather
// than parsing prose because a malformed plan is otherwise discovered halfway
// through executing it.
var planSchema = json.RawMessage(`{
  "type":"object",
  "properties":{
    "rationale":{"type":"string","description":"Why this decomposition, in two sentences."},
    "steps":{
      "type":"array","minItems":1,"maxItems":8,
      "items":{
        "type":"object",
        "properties":{
          "title":{"type":"string","description":"One concrete change, phrased as an instruction."},
          "scope":{"type":"array","minItems":1,"items":{"type":"string"},
            "description":"Path prefixes this step may change, relative to the repository root."},
          "verification":{"type":"string","enum":["low","standard","high"]},
          "depends_on":{"type":"array","items":{"type":"integer"},
            "description":"Indexes of earlier steps that must finish first."}
        },
        "required":["title","scope","verification"],
        "additionalProperties":false
      }
    }
  },
  "required":["rationale","steps"],
  "additionalProperties":false
}`)

const plannerPrompt = `Break one requirement into a small number of independent, verifiable steps.

Rules:
- Each step is one concrete change that can be verified on its own.
- Each step declares the narrowest scope that contains its change. Scope is how
  the supervisor detects a step doing more than it should, so "." is almost
  never right.
- Prefer fewer, larger steps over many tiny ones. Every step costs a full
  verification run.
- A step that only adds documentation or comments is verification level "low".
  A step touching concurrency, shared state or a public API is "high".
  Everything else is "standard".
- Order matters: if step B needs step A's change, list A first and set B's
  depends_on.

Do not invent files. Work from the code you are shown.`

// Planner decomposes requirements.
type Planner struct {
	Provider  llm.Provider
	Retriever *retrieval.Retriever
	Graph     graph.Graph
	// MaxSteps caps a plan. A decomposition into twenty steps is not a plan,
	// it is a to-do list, and each step costs a verification run.
	MaxSteps int
}

// DefaultMaxPlanSteps bounds a decomposition.
const DefaultMaxPlanSteps = 8

// Plan decomposes a requirement into child tasks.
func (p *Planner) Plan(ctx context.Context, req Requirement) (*Plan, error) {
	if p.Provider == nil {
		return nil, fmt.Errorf("task: planning needs a provider")
	}
	if !p.Provider.Capabilities().StructuredOutput {
		// Parsing a plan out of prose means a malformed plan is discovered
		// halfway through executing it. Refusing is the honest answer (DR-4).
		return nil, &llm.UnsupportedError{Provider: p.Provider.Name(), Capability: "structured output"}
	}
	maxSteps := p.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxPlanSteps
	}

	var brief strings.Builder
	fmt.Fprintf(&brief, "Requirement: %s\n", req.Title)
	if req.Body != "" {
		fmt.Fprintf(&brief, "\n%s\n", req.Body)
	}
	if len(req.Acceptance) > 0 {
		brief.WriteString("\nThis is done when:\n")
		for _, c := range req.Acceptance {
			fmt.Fprintf(&brief, "- %s\n", c.Name)
		}
	}

	plan := &Plan{RequirementID: req.ID, Objective: req.Title}

	// Ground the plan in the repository rather than in the model's guess at
	// what the code looks like.
	if p.Retriever != nil {
		pkt, err := p.Retriever.Build(ctx, retrieval.Request{Query: req.Title, ExpandDepth: 1})
		if err != nil {
			return nil, fmt.Errorf("task: planning retrieval: %w", err)
		}
		if len(pkt.Rejected) > 0 {
			return nil, fmt.Errorf("task: planning retrieval returned foreign slices: %s",
				strings.Join(pkt.Rejected, "; "))
		}
		if len(pkt.Slices) > 0 {
			brief.WriteString("\nRelevant code:\n")
			for _, s := range pkt.Slices {
				fmt.Fprintf(&brief, "\n%s:%d-%d\n", s.Path, s.StartLine, s.EndLine)
				if s.Signature != "" {
					fmt.Fprintf(&brief, "  %s\n", s.Signature)
				}
			}
		}
		plan.Impact = pkt.Impact
	}

	temp := 0.2
	resp, err := p.Provider.ChatStructured(ctx, llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: plannerPrompt},
			{Role: "user", Content: brief.String()},
		},
		Temperature: &temp,
	}, planSchema)
	if err != nil {
		return nil, fmt.Errorf("task: planning: %w", err)
	}

	var raw struct {
		Rationale string     `json:"rationale"`
		Steps     []PlanStep `json:"steps"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &raw); err != nil {
		return nil, fmt.Errorf("task: the planner returned output that is not a valid plan: %w", err)
	}
	plan.Rationale, plan.Steps = raw.Rationale, raw.Steps

	if err := plan.Validate(maxSteps); err != nil {
		return nil, err
	}
	return plan, nil
}

// Validate rejects a plan that cannot be safely executed.
//
// Every rule here exists because the failure it prevents is one that would
// otherwise be discovered mid-execution, with a half-applied change in a
// worktree.
func (p *Plan) Validate(maxSteps int) error {
	if len(p.Steps) == 0 {
		return fmt.Errorf("task: the plan has no steps")
	}
	if maxSteps > 0 && len(p.Steps) > maxSteps {
		return fmt.Errorf("task: the plan has %d steps, the limit is %d; "+
			"each step costs a full verification run", len(p.Steps), maxSteps)
	}

	for i, s := range p.Steps {
		if strings.TrimSpace(s.Title) == "" {
			return fmt.Errorf("task: step %d has no title", i+1)
		}
		if len(s.Scope) == 0 {
			return fmt.Errorf("task: step %d (%q) declares no scope; "+
				"without one the out-of-scope check cannot catch a step doing too much", i+1, s.Title)
		}
		for _, sc := range s.Scope {
			if strings.HasPrefix(sc, "/") || strings.Contains(sc, "..") {
				return fmt.Errorf("task: step %d declares scope %q, which escapes the repository", i+1, sc)
			}
		}
		if _, ok := recipe.ParseLevel(string(s.Verification)); !ok {
			return fmt.Errorf("task: step %d has verification level %q, which is not one of low, standard, high",
				i+1, s.Verification)
		}
		for _, dep := range s.DependsOn {
			if dep < 1 || dep > len(p.Steps) {
				return fmt.Errorf("task: step %d depends on step %d, which does not exist", i+1, dep)
			}
			if dep >= i+1 {
				// A forward or self dependency cannot be satisfied by running
				// the plan in order, and a cycle would deadlock the run.
				return fmt.Errorf("task: step %d depends on step %d, which does not come before it", i+1, dep)
			}
		}
	}
	return nil
}

// Materialise creates the child tasks a plan describes, in order, with their
// dependencies recorded.
func (s *Store) Materialise(ctx context.Context, plan *Plan, parentID string) ([]Task, error) {
	if err := plan.Validate(DefaultMaxPlanSteps); err != nil {
		return nil, err
	}
	ids := make([]string, len(plan.Steps))
	out := make([]Task, 0, len(plan.Steps))

	for i, step := range plan.Steps {
		t := Task{
			ID:            NewID("t"),
			RequirementID: plan.RequirementID,
			ParentID:      parentID,
			Title:         step.Title,
			Verification:  step.Verification,
			Budget: Budget{
				MaxAttempts: DefaultBudget().MaxAttempts,
				MaxWallTime: DefaultBudget().MaxWallTime,
				Scope:       step.Scope,
			},
		}
		if err := s.Create(ctx, t); err != nil {
			return nil, err
		}
		ids[i] = t.ID
		for _, dep := range step.DependsOn {
			if err := s.AddDependency(ctx, t.ID, ids[dep-1]); err != nil {
				return nil, err
			}
		}
		out = append(out, t)
	}
	return out, nil
}

// AddDependency records that one task must finish before another.
func (s *Store) AddDependency(ctx context.Context, taskID, dependsOn string) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`INSERT INTO task_deps (task_id, depends_on) VALUES (?,?)
			 ON CONFLICT DO NOTHING`, taskID, dependsOn)
		return err
	})
}

// Blockers returns the unfinished tasks a task depends on.
func (s *Store) Blockers(ctx context.Context, taskID string) ([]Task, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `
		SELECT d.depends_on FROM task_deps d
		JOIN tasks t ON t.id = d.depends_on
		WHERE d.task_id = ? AND t.state <> 'accepted'
		ORDER BY t.created_at`, taskID)
	if err != nil {
		return nil, err
	}
	var ids []string
	func() {
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
	}()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}
