package networth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/account"
	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/networth"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

const classes = "cash,savings,investments,p2p,pension,property,other,mortgage,loan"

type Handler struct {
	service *networth.Service
}

func NewHandler(service *networth.Service) *Handler { return &Handler{service: service} }

type AccountValue struct {
	AccountID uuid.UUID     `json:"account_id"`
	Class     string        `json:"class" enum:"cash,savings,investments,p2p,pension,property,other,mortgage,loan"`
	Liquidity string        `json:"liquidity,omitempty" enum:"immediate,days,locked" doc:"How soon the money could be spent; absent for a liability."`
	Valuation string        `json:"valuation" enum:"ledger,market,snapshot,schedule,flows" doc:"ledger: balance from transactions; market: plus positions at market prices; snapshot: values entered by hand or imported; schedule: the loan's repayment schedule; flows: a loan reserve, transfers in less out from a statement balance."`
	Value     money.Amount  `json:"value" doc:"In the account's currency, negative for a liability; a broker account's positions count only in value_czk."`
	ValueCZK  *money.Amount `json:"value_czk,omitempty" doc:"Absent without an exchange rate."`
	ValuedOn  *string       `json:"valued_on,omitempty" format:"date" doc:"The day of the value a snapshot account carries."`
	Unvalued  bool          `json:"unvalued" doc:"An asset never valued, or a loan without terms or a balance; it counts as zero."`
}

type KeyedAmount struct {
	Key   string       `json:"key"`
	Value money.Amount `json:"value"`
}

type NetWorth struct {
	On                string         `json:"on" format:"date"`
	TotalCZK          money.Amount   `json:"total_czk" doc:"Assets less liabilities."`
	AssetsCZK         money.Amount   `json:"assets_czk"`
	LiabilitiesCZK    money.Amount   `json:"liabilities_czk" doc:"Positive: what is owed."`
	CashCZK           money.Amount   `json:"cash_czk" doc:"Balances of accounts valued by their transactions, including brokers' cash and loan reserves."`
	InvestmentsCZK    money.Amount   `json:"investments_czk" doc:"Market value of the open positions."`
	OtherAssetsCZK    money.Amount   `json:"other_assets_czk" doc:"Assets valued at dates: P2P, pension, property and others."`
	ByClass           []KeyedAmount  `json:"by_class" doc:"key: class; liabilities are negative."`
	ByLiquidity       []KeyedAmount  `json:"by_liquidity" doc:"key: immediate, days or locked."`
	MonthlyExpenses   money.Amount   `json:"monthly_expenses_czk" doc:"Average spending of the last twelve complete months, without transfers and repayments."`
	ReserveMonths     *string        `json:"reserve_months,omitempty" doc:"Immediately available money in months of that spending; decimal, absent without spending."`
	Accounts          []AccountValue `json:"accounts"`
	Unconverted       []string       `json:"unconverted" doc:"Currencies held without a ČNB rate yet; their balances are left out."`
	UnvaluedPositions int            `json:"unvalued_positions" doc:"Positions without a price or rate, left out."`
}

type netWorthOutput struct {
	Body NetWorth
}

type historyInput struct {
	Months int `query:"months" minimum:"1" maximum:"120" default:"24"`
}

type NetWorthMonth struct {
	Month          string        `json:"month" doc:"The month whose end (today for the current month) the values are at."`
	On             string        `json:"on" format:"date"`
	TotalCZK       money.Amount  `json:"total_czk"`
	AssetsCZK      money.Amount  `json:"assets_czk"`
	LiabilitiesCZK money.Amount  `json:"liabilities_czk"`
	ChangeCZK      money.Amount  `json:"change_czk" doc:"Against the end of the month before."`
	SavedCZK       money.Amount  `json:"saved_czk" doc:"Income less spending of the month: the part of the change the user saved."`
	OtherChangeCZK money.Amount  `json:"other_change_czk" doc:"The rest of the change: returns, revaluation, repayments of loans valued by schedule."`
	ByClass        []KeyedAmount `json:"by_class"`
}

type historyOutput struct {
	Body struct {
		Months []NetWorthMonth `json:"months" doc:"Oldest first."`
	}
}

type accountInput struct {
	ID uuid.UUID `path:"id"`
}

type AccountValuation struct {
	ID       uuid.UUID    `json:"id"`
	ValuedOn string       `json:"valued_on" format:"date"`
	Value    money.Amount `json:"value" doc:"For a loan: the balance owed, positive."`
	Note     string       `json:"note"`
}

type valuationsOutput struct {
	Body struct {
		Valuations []AccountValuation `json:"valuations" doc:"Newest first."`
	}
}

type saveValuationInput struct {
	ID   uuid.UUID `path:"id"`
	Body struct {
		ValuedOn   string `json:"valued_on" format:"date"`
		ValueMinor int64  `json:"value_minor" minimum:"0" doc:"In the account's currency; for a loan the balance owed."`
		Note       string `json:"note,omitempty" maxLength:"200"`
	}
}

type valuationOutput struct {
	Body AccountValuation
}

type deleteValuationInput struct {
	ID          uuid.UUID `path:"id"`
	ValuationID uuid.UUID `path:"valuation_id"`
}

type TransferFlow struct {
	On     string       `json:"on" format:"date"`
	Amount money.Amount `json:"amount" doc:"Positive: put in; negative: taken out."`
}

type AccountPerformance struct {
	Contributed  money.Amount   `json:"contributed"`
	Withdrawn    money.Amount   `json:"withdrawn"`
	Value        *money.Amount  `json:"value,omitempty" doc:"The latest value; absent before the first."`
	ValuedOn     *string        `json:"valued_on,omitempty" format:"date"`
	Gain         *money.Amount  `json:"gain,omitempty" doc:"Value less the money put in net of what was taken out."`
	AnnualReturn *string        `json:"annual_return,omitempty" doc:"Money-weighted annual return (XIRR) as a decimal fraction, 0.085 = 8.5 %; absent when it cannot be told."`
	Flows        []TransferFlow `json:"flows" doc:"Transfers between this account and other own accounts, oldest first."`
}

type performanceOutput struct {
	Body AccountPerformance
}

type P2PLoan struct {
	LoanNumber        string        `json:"loan_number"`
	Name              string        `json:"name"`
	Repayment         string        `json:"repayment" doc:"As the platform names it, e.g. Balonově or Anuitně."`
	Status            string        `json:"status"`
	RepaymentStatus   string        `json:"repayment_status"`
	Invested          money.Amount  `json:"invested"`
	Outstanding       money.Amount  `json:"outstanding"`
	InterestRate      string        `json:"interest_rate" doc:"Percent a year, decimal."`
	InvestedOn        *string       `json:"invested_on,omitempty" format:"date"`
	NextPaymentOn     *string       `json:"next_payment_on,omitempty" format:"date"`
	NextPayment       *money.Amount `json:"next_payment,omitempty"`
	FinalPaymentOn    *string       `json:"final_payment_on,omitempty" format:"date"`
	DelayDays         int           `json:"delay_days"`
	Overdue           money.Amount  `json:"overdue"`
	InterestPaid      money.Amount  `json:"interest_paid"`
	PenaltyInterest   money.Amount  `json:"penalty_interest"`
	PrincipalReturned money.Amount  `json:"principal_returned"`
	MarketGain        money.Amount  `json:"market_gain"`
	Fees              money.Amount  `json:"fees"`
	AtRisk            bool          `json:"at_risk" doc:"Late, overdue or not repaid as agreed."`
}

type P2PMaturity struct {
	Month       *string      `json:"month,omitempty" doc:"Absent for loans without a final payment date."`
	Outstanding money.Amount `json:"outstanding"`
}

type P2PSnapshotTotal struct {
	On          string       `json:"on" format:"date"`
	Outstanding money.Amount `json:"outstanding"`
	Earned      money.Amount `json:"earned"`
}

type P2PSnapshot struct {
	On                  string             `json:"on" format:"date"`
	Outstanding         money.Amount       `json:"outstanding"`
	AtRisk              money.Amount       `json:"at_risk"`
	Earned              money.Amount       `json:"earned" doc:"Interest, penalties and market gains less fees, since investing."`
	EarnedSincePrevious *money.Amount      `json:"earned_since_previous,omitempty" doc:"Absent for the first export."`
	PreviousOn          *string            `json:"previous_on,omitempty" format:"date"`
	AverageRate         string             `json:"average_rate" doc:"Percent a year weighted by the outstanding principal."`
	Maturities          []P2PMaturity      `json:"maturities" doc:"Outstanding principal by the month of the final payment, oldest first."`
	History             []P2PSnapshotTotal `json:"history" doc:"Every imported export, oldest first."`
	Loans               []P2PLoan          `json:"loans"`
}

type p2pOutput struct {
	Body P2PSnapshot
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-net-worth", Method: http.MethodGet, Path: "/api/v1/reports/net-worth",
		Tags: []string{"Net worth"}, Summary: "What the user owns and owes today, per account and class, in CZK",
	}}, h.netWorth)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeReportsRead, Operation: huma.Operation{
		OperationID: "get-net-worth-history", Method: http.MethodGet, Path: "/api/v1/reports/net-worth/history",
		Tags: []string{"Net worth"}, Summary: "Net worth at each month's end, with what was saved and what the rest of the change was",
	}}, h.history)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "list-account-valuations", Method: http.MethodGet, Path: "/api/v1/accounts/{id}/valuations",
		Tags: []string{"Net worth"}, Summary: "Values entered by hand for an asset, or balances owed on a loan", Errors: []int{404, 422},
	}}, h.valuations)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "save-account-valuation", Method: http.MethodPost, Path: "/api/v1/accounts/{id}/valuations",
		Tags: []string{"Net worth"}, Summary: "Record an asset's value or a loan's balance at the end of a day",
		Description: "A second value for the same day replaces the first. A loan's balance corrects its schedule from that day on.",
		Errors:      []int{404, 422},
	}}, h.saveValuation)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "delete-account-valuation", Method: http.MethodDelete, Path: "/api/v1/accounts/{id}/valuations/{valuation_id}",
		Tags: []string{"Net worth"}, Summary: "Remove a value entered by hand", DefaultStatus: http.StatusNoContent, Errors: []int{404, 422},
	}}, h.deleteValuation)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "get-account-performance", Method: http.MethodGet, Path: "/api/v1/accounts/{id}/performance",
		Tags: []string{"Net worth"}, Summary: "What an asset valued at dates earned on the money transferred into it",
		Errors: []int{404, 422},
	}}, h.performance)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "get-p2p-loans", Method: http.MethodGet, Path: "/api/v1/accounts/{id}/p2p-loans",
		Tags: []string{"Net worth"}, Summary: "A P2P platform's loans from its latest imported export", Errors: []int{404},
	}}, h.p2p)
}

func czk(minor int64) money.Amount { return money.New(minor, money.CZK) }

func (h *Handler) netWorth(ctx context.Context, _ *struct{}) (*netWorthOutput, error) {
	s, err := h.service.Summary(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := NetWorth{
		On: dto.Date(s.On), TotalCZK: czk(s.NetCZKMinor()), AssetsCZK: czk(s.AssetsCZKMinor),
		LiabilitiesCZK: czk(s.LiabilitiesMinor), MonthlyExpenses: czk(s.MonthlyExpensesCZKMinor),
		UnvaluedPositions: s.UnvaluedPositions, Unconverted: make([]string, 0, len(s.Unconverted)),
		ByClass: amounts(s.ByClass), ByLiquidity: []KeyedAmount{}, Accounts: make([]AccountValue, 0, len(s.Accounts)),
	}
	for _, l := range []account.Liquidity{account.Immediate, account.Days, account.Locked} {
		if v, ok := s.ByLiquidity[l]; ok {
			out.ByLiquidity = append(out.ByLiquidity, KeyedAmount{Key: string(l), Value: czk(v)})
		}
	}
	if s.ReserveMonths != nil {
		months := s.ReserveMonths.String()
		out.ReserveMonths = &months
	}
	var cash, positions, other int64
	for _, v := range s.Accounts {
		out.Accounts = append(out.Accounts, toAccountValue(v))
		if v.ValueCZKMinor == nil {
			continue
		}
		switch v.Account.Kind.Valuation() {
		case account.ByLedger, account.ByFlows:
			cash += *v.ValueCZKMinor
		case account.ByMarket:
			cash += *v.ValueCZKMinor - v.PositionsCZKMinor
			positions += v.PositionsCZKMinor
		case account.BySnapshot:
			other += *v.ValueCZKMinor
		case account.BySchedule:
		}
	}
	out.CashCZK, out.InvestmentsCZK, out.OtherAssetsCZK = czk(cash), czk(positions), czk(other)
	for _, c := range s.Unconverted {
		out.Unconverted = append(out.Unconverted, string(c))
	}
	httpapi.CountResults(ctx, len(out.Accounts))
	return &netWorthOutput{Body: out}, nil
}

func amounts(byClass map[networth.Class]int64) []KeyedAmount {
	out := []KeyedAmount{}
	for _, c := range strings.Split(classes, ",") {
		if v, ok := byClass[networth.Class(c)]; ok {
			out = append(out, KeyedAmount{Key: c, Value: czk(v)})
		}
	}
	return out
}

func toAccountValue(v networth.AccountValue) AccountValue {
	out := AccountValue{AccountID: v.Account.ID, Class: string(v.Class), Liquidity: string(v.Liquidity),
		Valuation: string(v.Account.Kind.Valuation()), Value: money.New(v.ValueMinor, v.Account.Currency),
		ValuedOn: dto.DatePtr(v.ValuedOn), Unvalued: v.Unvalued}
	if v.ValueCZKMinor != nil {
		inCZK := czk(*v.ValueCZKMinor)
		out.ValueCZK = &inCZK
	}
	return out
}

func (h *Handler) history(ctx context.Context, in *historyInput) (*historyOutput, error) {
	months, err := h.service.History(ctx, in.Months)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &historyOutput{}
	out.Body.Months = make([]NetWorthMonth, 0, len(months))
	for _, m := range months {
		byClass := map[networth.Class]int64{}
		for _, v := range m.Accounts {
			if v.ValueCZKMinor != nil {
				byClass[v.Class] += *v.ValueCZKMinor
			}
		}
		out.Body.Months = append(out.Body.Months, NetWorthMonth{
			Month: dto.Month(m.On), On: dto.Date(m.On), TotalCZK: czk(m.NetCZKMinor()), AssetsCZK: czk(m.AssetsCZKMinor),
			LiabilitiesCZK: czk(m.LiabilitiesMinor), ChangeCZK: czk(m.ChangeCZKMinor), SavedCZK: czk(m.SavedCZKMinor),
			OtherChangeCZK: czk(m.OtherChangeCZKMinor()), ByClass: amounts(byClass),
		})
	}
	httpapi.CountResults(ctx, len(out.Body.Months))
	return out, nil
}

func mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, networth.ErrAccountNotFound), errors.Is(err, networth.ErrValuationNotFound),
		errors.Is(err, networth.ErrNoP2PSnapshot):
		return apierr.NotFound()
	case errors.Is(err, networth.ErrNotValuedByHand):
		return apierr.Unprocessable(apierr.CodeNotValuedByHand)
	default:
		return apierr.Internal(ctx, err)
	}
}

func (h *Handler) valuations(ctx context.Context, in *accountInput) (*valuationsOutput, error) {
	valuations, err := h.service.Valuations(ctx, in.ID)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	currency, err := h.accountCurrency(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	out := &valuationsOutput{}
	out.Body.Valuations = make([]AccountValuation, len(valuations))
	for i, v := range valuations {
		out.Body.Valuations[i] = toValuation(v, currency)
	}
	httpapi.CountResults(ctx, len(valuations))
	return out, nil
}

func (h *Handler) accountCurrency(ctx context.Context, id uuid.UUID) (money.Currency, error) {
	c, err := h.service.AccountCurrency(ctx, id)
	if err != nil {
		return "", mapError(ctx, err)
	}
	return c, nil
}

func toValuation(v networth.Valuation, currency money.Currency) AccountValuation {
	return AccountValuation{ID: v.ID, ValuedOn: dto.Date(v.ValuedOn), Value: money.New(v.ValueMinor, currency), Note: v.Note}
}

func (h *Handler) saveValuation(ctx context.Context, in *saveValuationInput) (*valuationOutput, error) {
	day, err := time.Parse(time.DateOnly, in.Body.ValuedOn)
	if err != nil {
		return nil, apierr.Validation("body.valued_on", "expected YYYY-MM-DD")
	}
	v := &networth.Valuation{AccountID: in.ID, ValuedOn: day, ValueMinor: in.Body.ValueMinor, Note: strings.TrimSpace(in.Body.Note)}
	if err := h.service.SaveValuation(ctx, v); err != nil {
		return nil, mapError(ctx, err)
	}
	currency, err := h.accountCurrency(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	return &valuationOutput{Body: toValuation(*v, currency)}, nil
}

func (h *Handler) deleteValuation(ctx context.Context, in *deleteValuationInput) (*struct{}, error) {
	if err := h.service.DeleteValuation(ctx, in.ID, in.ValuationID); err != nil {
		return nil, mapError(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) performance(ctx context.Context, in *accountInput) (*performanceOutput, error) {
	p, err := h.service.Performance(ctx, in.ID)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	currency, err := h.accountCurrency(ctx, in.ID)
	if err != nil {
		return nil, err
	}
	amount := func(minor int64) money.Amount { return money.New(minor, currency) }
	out := AccountPerformance{Contributed: amount(p.ContributedMinor), Withdrawn: amount(p.WithdrawnMinor),
		ValuedOn: dto.DatePtr(p.ValuedOn), Flows: make([]TransferFlow, len(p.Flows))}
	if p.ValueMinor != nil {
		value, gain := amount(*p.ValueMinor), amount(*p.GainMinor)
		out.Value, out.Gain = &value, &gain
	}
	if p.AnnualReturn != nil {
		r := p.AnnualReturn.String()
		out.AnnualReturn = &r
	}
	for i, f := range p.Flows {
		out.Flows[i] = TransferFlow{On: dto.Date(f.On), Amount: amount(f.AmountMinor)}
	}
	return &performanceOutput{Body: out}, nil
}

func (h *Handler) p2p(ctx context.Context, in *accountInput) (*p2pOutput, error) {
	s, err := h.service.P2P(ctx, in.ID)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	amount := func(minor int64) money.Amount { return money.New(minor, s.Currency) }
	out := P2PSnapshot{On: dto.Date(s.On), Outstanding: amount(s.OutstandingMinor), AtRisk: amount(s.AtRiskMinor),
		Earned: amount(s.EarnedMinor), PreviousOn: dto.DatePtr(s.PreviousOn), AverageRate: s.AverageRate.String(),
		Maturities: make([]P2PMaturity, len(s.Maturities)), History: make([]P2PSnapshotTotal, len(s.History)),
		Loans: make([]P2PLoan, len(s.Positions))}
	if s.EarnedSincePreviousMinor != nil {
		since := amount(*s.EarnedSincePreviousMinor)
		out.EarnedSincePrevious = &since
	}
	for i, m := range s.Maturities {
		var month *string
		if m.Month != nil {
			ym := dto.Month(*m.Month)
			month = &ym
		}
		out.Maturities[i] = P2PMaturity{Month: month, Outstanding: amount(m.OutstandingMinor)}
	}
	for i, t := range s.History {
		out.History[i] = P2PSnapshotTotal{On: dto.Date(t.On), Outstanding: amount(t.OutstandingMinor), Earned: amount(t.EarnedMinor)}
	}
	for i, p := range s.Positions {
		loan := P2PLoan{LoanNumber: p.LoanNumber, Name: p.Name, Repayment: p.Repayment, Status: p.Status,
			RepaymentStatus: p.RepaymentStatus, Invested: amount(p.InvestedMinor), Outstanding: amount(p.OutstandingMinor),
			InterestRate: p.InterestRate.String(), InvestedOn: dto.DatePtr(p.InvestedOn), NextPaymentOn: dto.DatePtr(p.NextPaymentOn),
			FinalPaymentOn: dto.DatePtr(p.FinalPaymentOn), DelayDays: p.DelayDays, Overdue: amount(p.OverdueMinor),
			InterestPaid: amount(p.InterestPaidMinor), PenaltyInterest: amount(p.PenaltyInterestMinor),
			PrincipalReturned: amount(p.PrincipalReturnedMinor), MarketGain: amount(p.MarketGainMinor), Fees: amount(p.FeesMinor),
			AtRisk: p.AtRisk()}
		if p.NextPaymentMinor != nil {
			next := amount(*p.NextPaymentMinor)
			loan.NextPayment = &next
		}
		out.Loans[i] = loan
	}
	httpapi.CountResults(ctx, len(out.Loans))
	return &p2pOutput{Body: out}, nil
}
