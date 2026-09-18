package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/akynte/local-engineer/internal/workflow"
)

func (s *Store) LoadWorkflow(ctx context.Context, id string) (*workflow.State, error) {
	var body string
	err := s.db.SQL().QueryRowContext(ctx, `SELECT body FROM task_workflow WHERE task_id = ?`, id).Scan(&body)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var state workflow.State
	if err := json.Unmarshal([]byte(body), &state); err != nil {
		return nil, err
	}
	return &state, nil
}

func (s *Store) SaveWorkflow(ctx context.Context, id string, state *workflow.State) error {
	body, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		var previous string
		err := tx.QueryRowContext(ctx, `SELECT phase FROM task_workflow WHERE task_id = ?`, id).Scan(&previous)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil && previous != string(state.Phase) {
			if err := workflow.Transition(workflow.Phase(previous), state.Phase); err != nil {
				return err
			}
		} else if errors.Is(err, sql.ErrNoRows) && state.Phase != workflow.Intake {
			return fmt.Errorf("workflow must start at INTAKE")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO task_workflow(task_id,phase,body,updated_at) VALUES (?,?,?,?)
		 ON CONFLICT(task_id) DO UPDATE SET phase=excluded.phase,body=excluded.body,updated_at=excluded.updated_at`,
			id, state.Phase, string(body), time.Now().UnixMilli())
		if err != nil {
			return err
		}
		if previous == string(state.Phase) {
			return nil
		}
		intent, _ := json.Marshal(map[string]any{"kind": "phase", "from": previous, "to": state.Phase})
		outcome, _ := json.Marshal(map[string]any{"phase": state.Phase})
		now := time.Now().UnixMilli()
		_, err = tx.ExecContext(ctx, `INSERT INTO operations(task_id,seq,kind,intent,outcome,candidate_before,candidate_after,started_at,finished_at)
		 VALUES (?,(SELECT COALESCE(MAX(seq),0)+1 FROM operations WHERE task_id=?),'decision',?,?,?,?,?,?)`,
			id, id, string(intent), string(outcome), state.Candidate, state.Candidate, now, now)
		return err
	})
}
