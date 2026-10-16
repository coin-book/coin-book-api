package importing

import (
	"coin-book-api/internal/domain/importing/statement"
)

type CheckKind string

const (
	CheckTotals       CheckKind = "totals"
	CheckBalanceChain CheckKind = "balance_chain"
	CheckDeclaredSum  CheckKind = "declared_sum"
)

type Check struct {
	Kind          CheckKind `json:"kind"`
	OK            bool      `json:"ok"`
	ExpectedMinor int64     `json:"expected_minor"`
	ActualMinor   int64     `json:"actual_minor"`
	// Line is the first source line where a balance chain breaks; zero otherwise.
	Line int `json:"line,omitempty"`
}

// runChecks verifies a statement account against the control data the file
// itself carries (F-IMP-3); which checks apply depends on the format.
func runChecks(acc *statement.Account) []Check {
	var checks []Check
	var income, expense, sum int64
	for _, r := range acc.Rows {
		sum += r.AmountMinor
		if r.AmountMinor > 0 {
			income += r.AmountMinor
		} else {
			expense += r.AmountMinor
		}
	}
	if acc.Totals != nil {
		checks = append(checks,
			Check{Kind: CheckTotals, OK: acc.Totals.IncomeMinor == income, ExpectedMinor: acc.Totals.IncomeMinor, ActualMinor: income},
			Check{Kind: CheckTotals, OK: acc.Totals.ExpenseMinor == expense, ExpectedMinor: acc.Totals.ExpenseMinor, ActualMinor: expense})
	}
	if acc.DeclaredSum != nil {
		checks = append(checks, Check{Kind: CheckDeclaredSum, OK: *acc.DeclaredSum == sum, ExpectedMinor: *acc.DeclaredSum, ActualMinor: sum})
	}
	if chain, ok := balanceChain(acc.Rows); ok {
		checks = append(checks, chain)
	}
	return checks
}

func balanceChain(rows []statement.Row) (Check, bool) {
	if len(rows) < 2 || rows[0].BalanceAfterMinor == nil {
		return Check{}, false
	}
	for i := 1; i < len(rows); i++ {
		prev, cur := rows[i-1].BalanceAfterMinor, rows[i].BalanceAfterMinor
		if prev == nil || cur == nil {
			continue
		}
		if expected := *prev + rows[i].AmountMinor; expected != *cur {
			return Check{Kind: CheckBalanceChain, ExpectedMinor: expected, ActualMinor: *cur, Line: rows[i].Line}, true
		}
	}
	last := *rows[len(rows)-1].BalanceAfterMinor
	return Check{Kind: CheckBalanceChain, OK: true, ExpectedMinor: last, ActualMinor: last}, true
}

func allPassed(checks []Check) bool {
	for _, c := range checks {
		if !c.OK {
			return false
		}
	}
	return true
}
