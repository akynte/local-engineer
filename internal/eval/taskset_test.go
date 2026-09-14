package eval_test

// The task set is as much a part of the measurement as the harness. A task
// that cannot be solved, or that a wrong answer passes, silently changes what
// the numbers mean.
//
// Every task here is checked two ways: a known-correct solution must pass, and
// a plausible wrong one must fail. Without the second check a task that always
// passes would look like an easy task rather than a broken one.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/eval"
)

func taskSetDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		candidate := filepath.Join(dir, "evals", "tasks")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("evals/tasks not found")
	return ""
}

func loadSet(t *testing.T) []eval.Task {
	t.Helper()
	tasks, err := eval.LoadSet(taskSetDir(t))
	if err != nil {
		t.Fatalf("the task set does not load: %v", err)
	}
	return tasks
}

// Every shipped task must validate, or the set silently shrinks.
func TestShippedTaskSetValidates(t *testing.T) {
	tasks := loadSet(t)
	if len(tasks) == 0 {
		t.Fatal("the task set is empty")
	}
	for _, task := range tasks {
		if err := task.Validate(); err != nil {
			t.Errorf("%v", err)
		}
		if task.Notes == "" {
			t.Errorf("%s: no notes; a reader of the results needs to know what a "+
				"wrong-but-passing solution looks like", task.ID)
		}
		if _, err := os.Stat(task.FixturePath()); err != nil {
			t.Errorf("%s: fixture missing: %v", task.ID, err)
		}
	}
}

// The objective must not give away the fix. An objective that says what to
// change measures typing, not engineering.
func TestObjectivesDoNotContainTheAnswer(t *testing.T) {
	giveaways := []string{"x + y", "x - y", "return nil", "ErrNotFound"}
	for _, task := range loadSet(t) {
		lower := strings.ToLower(task.Objective)
		for _, g := range giveaways {
			if strings.Contains(lower, strings.ToLower(g)) {
				t.Errorf("%s: the objective contains %q, which gives away the fix", task.ID, g)
			}
		}
	}
}

// A fixture must start in a state where its own visible tests pass. A fixture
// that is already failing makes the task ambiguous: the solver cannot tell
// which failure it was asked to fix.
func TestFixturesStartGreenOnTheirVisibleTests(t *testing.T) {
	requireGo(t)
	for _, task := range loadSet(t) {
		t.Run(task.ID, func(t *testing.T) {
			dir := t.TempDir()
			copyFixture(t, task.FixturePath(), dir)

			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
				t.Skip("not a Go fixture")
			}
			cmd := exec.Command("go", "test", "./...")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("the fixture's own tests fail before any work is done:\n%s", out)
			}
		})
	}
}

// The acceptance command must fail on the untouched fixture. A task whose
// acceptance already passes measures nothing at all, and would look like a
// task every arm solves.
func TestAcceptanceFailsOnTheUntouchedFixture(t *testing.T) {
	requireGo(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	for _, task := range loadSet(t) {
		t.Run(task.ID, func(t *testing.T) {
			out := r.Run(context.Background(), task, eval.Arm{Name: "noop"},
				solverFunc(func(context.Context, eval.SolveRequest) (eval.SolveResult, error) {
					return eval.SolveResult{}, nil // change nothing
				}))
			if out.Errored() {
				t.Fatalf("run errored: %s", out.Err)
			}
			if out.Solved {
				t.Fatal("the acceptance command passes on the untouched fixture; " +
					"this task measures nothing")
			}
		})
	}
}

// And a known-correct solution must pass, or the task is unsolvable and every
// arm scores zero on it for reasons that have nothing to do with the system.
func TestKnownGoodSolutionsPass(t *testing.T) {
	requireGo(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	solutions := map[string]eval.Solver{
		"nil-deref-001":        solverFunc(solveNilDeref),
		"signature-change-001": solverFunc(solveSignatureChange),
		"config-rename-001":    solverFunc(solveConfigRename),

		"reconcile-steals-reservations": solverFunc(solveReconcileSteals),
		"conflict-status-001":           solverFunc(solveConflictStatus),
		"perishable-zone-001":           solverFunc(solvePerishableZone),
		"damaged-stock-001":             solverFunc(solveDamagedStock),
		"reserve-idempotent-001":        solverFunc(solveReserveIdempotent),
	}

	for _, task := range loadSet(t) {
		solver, ok := solutions[task.ID]
		if !ok {
			t.Errorf("%s has no reference solution; a task nobody has solved "+
				"may be unsolvable, and every arm would score zero for the wrong reason", task.ID)
			continue
		}
		t.Run(task.ID, func(t *testing.T) {
			out := r.Run(context.Background(), task, eval.Arm{Name: "reference"}, solver)
			if out.Errored() {
				t.Fatalf("run errored: %s", out.Err)
			}
			if !out.Solved {
				t.Fatalf("the reference solution does not pass:\n%s", out.AcceptanceOutput)
			}
		})
	}
}

// The reference solutions. Each is what a correct answer looks like; the tests
// above use them to prove the tasks are solvable and the checks discriminate.

func solveNilDeref(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	path := filepath.Join(req.Worktree, "internal/service/user.go")
	body, err := os.ReadFile(path)
	if err != nil {
		return eval.SolveResult{}, err
	}
	fixed := strings.Replace(string(body),
		"	u := s.store.Find(id)\n	return u.Email, nil",
		"	u := s.store.Find(id)\n	if u == nil {\n		return \"\", ErrNotFound\n	}\n	return u.Email, nil", 1)
	return eval.SolveResult{Claimed: true, Attempts: 1},
		os.WriteFile(path, []byte(fixed), 0o644)
}

func solveSignatureChange(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	edits := map[string][2]string{
		"internal/billing/total.go": {
			"func Total(lines []Line) int {\n	total := 0\n	for _, l := range lines {\n		total += l.PenceEach * l.Quantity\n	}\n	return total\n}",
			"func Total(lines []Line, currency string) (int, string) {\n	total := 0\n	for _, l := range lines {\n		total += l.PenceEach * l.Quantity\n	}\n	return total, currency\n}",
		},
		"internal/report/monthly.go": {
			"		sum += billing.Total(lines)",
			"		amount, _ := billing.Total(lines, \"GBP\")\n		sum += amount",
		},
		"internal/api/invoice.go": {
			"	return InvoiceResponse{TotalPence: billing.Total(lines)}",
			"	total, _ := billing.Total(lines, \"GBP\")\n	return InvoiceResponse{TotalPence: total}",
		},
		"internal/billing/total_test.go": {
			"	if got := Total(lines); got != 250 {",
			"	if got, _ := Total(lines, \"GBP\"); got != 250 {",
		},
	}
	for rel, edit := range edits {
		path := filepath.Join(req.Worktree, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return eval.SolveResult{}, err
		}
		updated := strings.Replace(string(body), edit[0], edit[1], 1)
		if updated == string(body) {
			return eval.SolveResult{}, os.ErrInvalid
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

func solveConfigRename(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	for _, rel := range []string{"internal/config/config.go", "docker-compose.yml", "Dockerfile"} {
		path := filepath.Join(req.Worktree, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return eval.SolveResult{}, err
		}
		updated := strings.ReplaceAll(string(body), "DB_URL", "DATABASE_URL")
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// solveReconcileSteals records that a cancellation already returned its stock,
// so the reconciler can tell that case from an order orphaned by a crash. Any
// fix that makes the same distinction is equally correct; this one exists to
// prove the task is solvable and that the hidden tests discriminate.
func solveReconcileSteals(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	edits := map[string][2]string{
		"internal/domain/order.go": {
			"	// ShippingZone is resolved at placement and frozen, so a later change to\n" +
				"	// zone definitions does not silently reprice a shipped order.\n" +
				"	ShippingZone string\n}",
			"	// ShippingZone is resolved at placement and frozen, so a later change to\n" +
				"	// zone definitions does not silently reprice a shipped order.\n" +
				"	ShippingZone string\n" +
				"	// StockReleased records that this order's reservation has already been\n" +
				"	// returned to the available pool. The Reserved counter is shared across\n" +
				"	// orders, so releasing twice takes stock from whoever else is holding it.\n" +
				"	StockReleased bool\n}",
		},
		"internal/orders/orders.go": {
			"	for _, line := range order.Lines {\n" +
				"		if err := s.inventory.Release(ctx, line.SKU, line.Quantity); err != nil {\n" +
				"			return err\n		}\n	}\n" +
				"	if err := s.orders.PutOrder(ctx, order); err != nil {",
			"	for _, line := range order.Lines {\n" +
				"		if err := s.inventory.Release(ctx, line.SKU, line.Quantity); err != nil {\n" +
				"			return err\n		}\n	}\n" +
				"	order.StockReleased = true\n" +
				"	if err := s.orders.PutOrder(ctx, order); err != nil {",
		},
		"internal/worker/reconcile.go": {
			"	for _, order := range cancelled {\n		for _, line := range order.Lines {",
			"	for _, order := range cancelled {\n" +
				"		if order.StockReleased {\n			continue\n		}\n" +
				"		for _, line := range order.Lines {",
		},
	}
	for rel, pair := range edits {
		path := filepath.Join(req.Worktree, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return eval.SolveResult{}, err
		}
		if !strings.Contains(string(body), pair[0]) {
			return eval.SolveResult{}, fmt.Errorf("reference solution: %s does not contain the expected text", rel)
		}
		updated := strings.Replace(string(body), pair[0], pair[1], 1)
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
	}

	// Mark the order released once the reconciler has recovered it, so a later
	// pass does not release it again.
	path := filepath.Join(req.Worktree, "internal/worker/reconcile.go")
	body, err := os.ReadFile(path)
	if err != nil {
		return eval.SolveResult{}, err
	}
	old := "			rep.ReservationsFreed += line.Quantity\n		}\n	}"
	next := "			rep.ReservationsFreed += line.Quantity\n		}\n" +
		"		order.StockReleased = true\n" +
		"		if err := r.orders.PutOrder(ctx, order); err != nil {\n" +
		"			rep.Discrepancies = append(rep.Discrepancies, fmt.Sprintf(\"marking %s: %v\", order.ID, err))\n" +
		"		}\n	}"
	if !strings.Contains(string(body), old) {
		return eval.SolveResult{}, fmt.Errorf("reference solution: reconcile.go does not contain the release loop")
	}
	updated := strings.Replace(string(body), old, next, 1)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return eval.SolveResult{}, err
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

// replaceIn applies one exact substitution to a file in the worktree.
func replaceIn(worktree, rel, from, to string) error {
	path := filepath.Join(worktree, filepath.FromSlash(rel))
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if !strings.Contains(string(body), from) {
		return fmt.Errorf("reference solution: %s does not contain the expected text", rel)
	}
	return os.WriteFile(path, []byte(strings.Replace(string(body), from, to, 1)), 0o644)
}

// solveConflictStatus adds the missing case to the API's error mapping.
func solveConflictStatus(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	err := replaceIn(req.Worktree, "internal/api/api.go",
		"	case errors.Is(err, store.ErrNotFound):",
		"	case errors.Is(err, store.ErrConflict):\n"+
			"		writeJSON(w, http.StatusConflict, map[string]string{\"error\": \"version conflict, retry\"})\n"+
			"	case errors.Is(err, store.ErrNotFound):")
	if err != nil {
		return eval.SolveResult{}, err
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

// solvePerishableZone refuses perishable parcels in zones with no cold chain,
// reusing the carrier refusal the API already maps to a 409.
func solvePerishableZone(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	err := replaceIn(req.Worktree, "internal/shipping/shipping.go",
		"	if p.RequiresSpecial && !zone.SpecialHandling {\n"+
			"		return domain.Money{}, fmt.Errorf(\"%w: zone %s\", ErrNoCarrier, zone.Name)\n	}",
		"	if p.RequiresSpecial && !zone.SpecialHandling {\n"+
			"		return domain.Money{}, fmt.Errorf(\"%w: zone %s\", ErrNoCarrier, zone.Name)\n	}\n"+
			"	// Only the domestic network is refrigerated, and it is the same zones\n"+
			"	// that take special handling.\n"+
			"	if p.ContainsPerishable && !zone.SpecialHandling {\n"+
			"		return domain.Money{}, fmt.Errorf(\"%w: zone %s has no cold chain\", ErrNoCarrier, zone.Name)\n	}")
	if err != nil {
		return eval.SolveResult{}, err
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

// solveDamagedStock adds the count, excludes it from availability while leaving
// the physical on-hand figure alone, and gives inventory a way to record it.
func solveDamagedStock(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	if err := replaceIn(req.Worktree, "internal/store/store.go",
		"	// Version guards against a lost update when two reservations race.\n"+
			"	Version int\n}",
		"	// Damaged counts units that are present but unsellable. They stay in\n"+
			"	// OnHand because they are physically here, and come out of Available\n"+
			"	// because they cannot be promised to anyone.\n"+
			"	Damaged int\n"+
			"	// Version guards against a lost update when two reservations race.\n"+
			"	Version int\n}"); err != nil {
		return eval.SolveResult{}, err
	}
	if err := replaceIn(req.Worktree, "internal/store/store.go",
		"func (s Stock) Available() int { return s.OnHand - s.Reserved }",
		"func (s Stock) Available() int { return s.OnHand - s.Reserved - s.Damaged }"); err != nil {
		return eval.SolveResult{}, err
	}
	if err := replaceIn(req.Worktree, "internal/inventory/inventory.go",
		"// Receive adds newly delivered units.",
		"// MarkDamaged records units that are present but unsellable.\n"+
			"func (s *Service) MarkDamaged(ctx context.Context, sku domain.SKU, quantity int) error {\n"+
			"	if quantity < 0 {\n"+
			"		return fmt.Errorf(\"inventory: damaged count cannot be negative, got %d\", quantity)\n	}\n"+
			"	st, err := s.stock.GetStock(ctx, sku)\n"+
			"	if err != nil {\n		return err\n	}\n"+
			"	st.Damaged = quantity\n"+
			"	return s.stock.PutStock(ctx, st)\n}\n\n"+
			"// Receive adds newly delivered units."); err != nil {
		return eval.SolveResult{}, err
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

// solveReserveIdempotent gives Reserve a holder key and records which holders a
// SKU has already reserved for, so a repeat is recognised and two distinct
// orders still both reserve.
func solveReserveIdempotent(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	if err := replaceIn(req.Worktree, "internal/store/store.go",
		"	// Version guards against a lost update when two reservations race.\n"+
			"	Version int\n}",
		"	// Holders names the orders this row has already reserved for, so a\n"+
			"	// repeated request is recognised rather than counted twice.\n"+
			"	Holders map[string]int\n"+
			"	// Version guards against a lost update when two reservations race.\n"+
			"	Version int\n}"); err != nil {
		return eval.SolveResult{}, err
	}
	if err := replaceIn(req.Worktree, "internal/inventory/inventory.go",
		"// Reserve holds stock for an order. It is called once per line.\n"+
			"func (s *Service) Reserve(ctx context.Context, sku domain.SKU, quantity int) error {\n"+
			"	if quantity <= 0 {\n"+
			"		return fmt.Errorf(\"inventory: quantity must be positive, got %d\", quantity)\n	}\n"+
			"	st, err := s.stock.GetStock(ctx, sku)\n"+
			"	if err != nil {\n		return err\n	}\n",
		"// Reserve holds stock for an order. holder identifies the order, so the\n"+
			"// same request arriving twice reserves once.\n"+
			"func (s *Service) Reserve(ctx context.Context, sku domain.SKU, holder string, quantity int) error {\n"+
			"	if quantity <= 0 {\n"+
			"		return fmt.Errorf(\"inventory: quantity must be positive, got %d\", quantity)\n	}\n"+
			"	st, err := s.stock.GetStock(ctx, sku)\n"+
			"	if err != nil {\n		return err\n	}\n"+
			"	if holder != \"\" {\n"+
			"		if already, seen := st.Holders[holder]; seen && already >= quantity {\n"+
			"			return nil\n		}\n	}\n"); err != nil {
		return eval.SolveResult{}, err
	}
	if err := replaceIn(req.Worktree, "internal/inventory/inventory.go",
		"	st.Reserved += quantity\n	return s.stock.PutStock(ctx, st)",
		"	st.Reserved += quantity\n"+
			"	if holder != \"\" {\n"+
			"		if st.Holders == nil {\n			st.Holders = map[string]int{}\n		}\n"+
			"		st.Holders[holder] += quantity\n	}\n"+
			"	return s.stock.PutStock(ctx, st)"); err != nil {
		return eval.SolveResult{}, err
	}
	if err := replaceIn(req.Worktree, "internal/orders/orders.go",
		"		if err := s.inventory.Reserve(ctx, line.SKU, line.Quantity); err != nil {",
		"		if err := s.inventory.Reserve(ctx, line.SKU, string(order.ID), line.Quantity); err != nil {"); err != nil {
		return eval.SolveResult{}, err
	}
	// The fixture's own inventory tests call the old signature.
	for _, from := range []string{
		"svc.Reserve(ctx, \"AB-1234\", 3)",
		"svc.Reserve(context.Background(), \"AB-1234\", 99)",
		"svc.Reserve(ctx, \"AB-1234\", 4)",
	} {
		to := strings.Replace(from, "\"AB-1234\", ", "\"AB-1234\", \"t\", ", 1)
		if err := replaceIn(req.Worktree, "internal/inventory/inventory_test.go", from, to); err != nil {
			return eval.SolveResult{}, err
		}
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}
