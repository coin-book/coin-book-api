package importing

import (
	"context"
	"strconv"

	"github.com/google/uuid"
	"github.com/rotisserie/eris"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/bank"
	"coin-book-api/internal/domain/importing/statement"
	"coin-book-api/internal/platform/db"
)

var (
	ErrUnknownBank       = eris.New("importing: the chosen bank is not in the register")
	ErrMappingRequired   = eris.New("importing: the file does not say which account it is; choose one")
	ErrCurrencyMismatch  = eris.New("importing: the chosen account holds a different currency")
	ErrUnknownAccount    = eris.New("importing: the chosen account does not exist")
	ErrInvalidNewAccount = eris.New("importing: a new account needs a name")
)

// Target is where one statement account's rows go: an existing account, or a new one.
// Exactly one of the two is set.
type Target struct {
	AccountID *uuid.UUID
	Create    *NewAccount
}

type NewAccount struct {
	Name string
	Kind account.Kind
	// BankID: nil lets the import pick the bank from the parser or the account number.
	BankID *string
}

// Mapping is keyed by AccountResult.Key.
type Mapping map[string]Target

type MatchedBy string

const (
	MatchedByIdentity MatchedBy = "identity"
	MatchedByAlias    MatchedBy = "alias"
	MatchedByUser     MatchedBy = "user"
	MatchedByNone     MatchedBy = "none"
)

const unidentifiedKeyPrefix = "file:"

// resolved is one statement account with the decision where it goes.
type resolved struct {
	src       *statement.Account
	key       string
	existing  *account.Account
	create    *account.Account
	matchedBy MatchedBy
	// idIdentity seeds the transaction ids: the bank's own identity when the file has
	// one, otherwise the target account's, so re-importing into the same account dedupes.
	idIdentity string
}

func sourceKey(acc *statement.Account, index int) string {
	if id := acc.Ref.Identity(); id != "" {
		return id
	}
	return unidentifiedKeyPrefix + strconv.Itoa(index)
}

func (s *Service) resolve(ctx context.Context, q db.Querier, acc *statement.Account, key string, mapping Mapping, defaultName string) (*resolved, error) {
	r := &resolved{src: acc, key: key}
	if target, ok := mapping[key]; ok {
		r.matchedBy = MatchedByUser
		return r, s.apply(ctx, q, r, target)
	}
	if identity := acc.Ref.Identity(); identity != "" {
		alias, err := s.accounts.FindAlias(ctx, q, string(acc.Ref.Institution), identity)
		if err != nil {
			return nil, err
		}
		if alias != nil {
			r.existing, r.matchedBy, r.idIdentity = alias, MatchedByAlias, identity
			return r, nil
		}
		existing, err := s.accounts.FindByIdentity(ctx, q, acc.Ref.AccountNumber, acc.Ref.ExternalRef)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			r.existing, r.matchedBy, r.idIdentity = existing, MatchedByIdentity, identity
			return r, nil
		}
	}
	r.matchedBy = MatchedByNone
	name := acc.Ref.Name
	if name == "" {
		name = defaultName
	}
	return r, s.apply(ctx, q, r, Target{Create: &NewAccount{Name: name, Kind: acc.Ref.Kind}})
}

func (s *Service) apply(ctx context.Context, q db.Querier, r *resolved, target Target) error {
	identity := r.src.Ref.Identity()
	if target.AccountID != nil {
		existing, err := s.accounts.GetIn(ctx, q, *target.AccountID)
		if err != nil {
			return ErrUnknownAccount
		}
		if existing.Currency != r.src.Ref.Currency {
			return eris.Wrapf(ErrCurrencyMismatch, "%s into %s", r.src.Ref.Currency, existing.Currency)
		}
		r.existing = existing
		r.idIdentity = identity
		if identity == "" {
			r.idIdentity = existing.Identity()
		}
		return nil
	}
	if target.Create == nil || target.Create.Name == "" {
		return ErrInvalidNewAccount
	}
	created := &account.Account{
		Name: target.Create.Name, Institution: r.src.Ref.Institution, Kind: target.Create.Kind,
		Currency: r.src.Ref.Currency, AccountNumber: r.src.Ref.AccountNumber, ExternalRef: r.src.Ref.ExternalRef,
		OpeningBalanceMinor: openingBalance(r.src),
	}
	if created.Kind == "" {
		created.Kind = account.Current
	}
	if identity == "" {
		ref := account.ManualRef()
		created.ExternalRef = &ref
		identity = ref
	}
	bankID, err := s.bankFor(ctx, q, r.src.Ref, target.Create.BankID)
	if err != nil {
		return err
	}
	created.BankID = bankID
	r.create, r.idIdentity = created, identity
	return nil
}

// bankFor takes the user's choice, else the bank the parser implies, else the one the
// account number names.
func (s *Service) bankFor(ctx context.Context, q db.Querier, ref statement.AccountRef, chosen *string) (*string, error) {
	if chosen != nil {
		if *chosen == "" {
			return nil, nil
		}
		known, err := bank.Known(ctx, q, *chosen)
		if err != nil {
			return nil, err
		}
		if known == nil {
			return nil, ErrUnknownBank
		}
		return known, nil
	}
	switch ref.Institution {
	case account.Revolut:
		return bank.Known(ctx, q, bank.Revolut)
	case account.IBKR:
		return bank.Known(ctx, q, bank.IBKR)
	case account.MBankCZ, account.MBankSK, account.Fingood, account.Investown, account.Portu, account.Other:
	}
	return bank.Detect(ctx, q, ref.AccountNumber)
}

// rememberAlias records a user's choice to put a bank account into an account that
// does not carry its identity, so the next import of it is mapped the same way.
func (s *Service) rememberAlias(ctx context.Context, q db.Querier, r *resolved, accountID uuid.UUID) error {
	identity := r.src.Ref.Identity()
	if identity == "" || r.matchedBy != MatchedByUser || r.existing == nil || r.existing.Identity() == identity {
		return nil
	}
	return s.accounts.UpsertAlias(ctx, q, string(r.src.Ref.Institution), identity, accountID)
}
