package categorization

import (
	"time"

	"github.com/google/uuid"
	"github.com/rotisserie/eris"
)

type Kind string

const (
	Income    Kind = "income"
	Expense   Kind = "expense"
	Financial Kind = "financial"
	Transfer  Kind = "transfer"
)

type Source string

const (
	SourceRule   Source = "rule"
	SourceManual Source = "manual"
	SourceBank   Source = "bank"
	// SourceModel is the learned classifier's guess above the auto threshold, awaiting confirmation.
	SourceModel Source = "model"
	// SourceLoan is a loan instalment split into interest and principal by its schedule (ADR 211).
	SourceLoan Source = "loan"
)

var (
	ErrCategoryNotFound = eris.New("categorization: category not found")
	ErrRuleNotFound     = eris.New("categorization: rule not found")
	ErrSplitMismatch    = eris.New("categorization: split lines do not add up to the transaction amount")
	ErrNoLines          = eris.New("categorization: at least one category line is required")
	ErrNothingToMatch   = eris.New("categorization: the transaction has no value the rule could match on")
	ErrInvalidParent    = eris.New("categorization: a subcategory belongs under a main category other than itself")
	ErrInvalidMerge     = eris.New("categorization: a category cannot be merged into itself or its own subcategory")
	ErrCategoryInUse    = eris.New("categorization: the category is in use; choose a category to move its data to")
	ErrSystemCategory   = eris.New("categorization: a system category can only change its look")
)

type Category struct {
	ID            uuid.UUID
	Code          string
	ParentID      *uuid.UUID
	NameCS        string
	NameSK        string
	NameEN        string
	Kind          Kind
	ColorKey      *string
	Icon          *string
	BudgetGroupID *uuid.UUID
	Sort          int
	// Custom marks the user's own category; system categories keep their name, kind and place.
	Custom bool
}

type BudgetGroup struct {
	ID     uuid.UUID
	Code   string
	NameCS string
	NameSK string
	NameEN string
	Sort   int
}

type Rule struct {
	ID                       uuid.UUID
	CategoryID               uuid.UUID
	Priority                 int
	MatchCounterparty        *string
	MatchDescription         *string
	MatchCounterpartyAccount *string
	MatchOperationType       *string
	MatchVariableSymbol      *string
	CreatedAt                time.Time
}

// Candidate is the part of a transaction rules look at.
type Candidate struct {
	TransactionID       string
	AmountMinor         int64
	Description         string
	Counterparty        string
	CounterpartyAccount string
	OperationType       string
	VariableSymbol      string
	BankCategory        string
	// CategoryHint is a category code or name the imported file assigned itself.
	CategoryHint string
}

type Line struct {
	CategoryID  uuid.UUID
	AmountMinor int64
}
