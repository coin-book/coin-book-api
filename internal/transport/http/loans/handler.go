package loans

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"coin-book-api/internal/domain/agentaccess"
	"coin-book-api/internal/domain/loan"
	"coin-book-api/internal/platform/db"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

// maxRate keeps the rate within numeric(7,4) and sense.
var maxRate = decimal.NewFromInt(100)

// OwnTransferDetector recognises the bank's transfers into a reserve that was just created.
type OwnTransferDetector interface {
	DetectOwn(ctx context.Context, q db.Querier, transactionIDs []string) (int, error)
}

type Handler struct {
	service   *loan.Service
	transfers OwnTransferDetector
	pool      *db.Pool
}

func NewHandler(service *loan.Service, transfers OwnTransferDetector, pool *db.Pool) *Handler {
	return &Handler{service: service, transfers: transfers, pool: pool}
}

type LoanTerms struct {
	ValidFrom      string       `json:"valid_from" format:"date"`
	Balance        money.Amount `json:"balance" doc:"Owed on valid_from."`
	AnnualRate     string       `json:"annual_rate" doc:"Percent a year, decimal." example:"4.89"`
	LoanPaymentRow money.Amount `json:"payment" doc:"The monthly instalment."`
	PaymentDay     int          `json:"payment_day" doc:"Day of the month the instalment is due; the month's last day when shorter."`
	MaturityOn     string       `json:"maturity_on" format:"date"`
	FixedUntil     *string      `json:"fixed_until,omitempty" format:"date" doc:"Absent for a rate that is not fixed."`
}

type LoanInstalment struct {
	DueOn     string       `json:"due_on" format:"date"`
	Interest  money.Amount `json:"interest"`
	Principal money.Amount `json:"principal"`
	Balance   money.Amount `json:"balance" doc:"Owed after this instalment."`
	Status    string       `json:"status" enum:"paid,assumed,projected" doc:"paid: bank payments booked nearest its due day paid it; assumed: due by today without payments, counted as paid by the schedule; projected: still to come."`
}

type LoanPaymentRow struct {
	TransactionID string       `json:"transaction_id"`
	BookedOn      string       `json:"booked_on" format:"date"`
	Description   string       `json:"description"`
	Amount        money.Amount `json:"amount" doc:"Positive: paid."`
	Interest      money.Amount `json:"interest"`
	Principal     money.Amount `json:"principal"`
	BySchedule    bool         `json:"by_schedule" doc:"The split follows the schedule; false: the user split it."`
}

type LoanInterestYear struct {
	Year       int          `json:"year"`
	Interest   money.Amount `json:"interest" doc:"Interest of the instalments due that year by today."`
	Deductible money.Amount `json:"deductible" doc:"What may lower the tax base for one's own housing (mortgages only): 150 000 Kč a year, 300 000 Kč for housing financed before 2021."`
}

type LoanOverview struct {
	AccountID         uuid.UUID          `json:"account_id"`
	Kind              string             `json:"kind" enum:"mortgage,loan"`
	Balance           money.Amount       `json:"balance" doc:"Owed today."`
	Current           *LoanTerms         `json:"current,omitempty" doc:"The terms in force today; absent before the first."`
	Next              *LoanInstalment    `json:"next,omitempty" doc:"The next instalment after today; absent once repaid."`
	RemainingMonths   int                `json:"remaining_months"`
	RemainingInterest money.Amount       `json:"remaining_interest" doc:"Interest of the projected instalments."`
	PaidOffOn         *string            `json:"paid_off_on,omitempty" format:"date" doc:"The last instalment's due day."`
	Terms             []LoanTerms        `json:"terms" doc:"Oldest first."`
	Payments          []LoanPaymentRow   `json:"payments" doc:"Newest first."`
	Years             []LoanInterestYear `json:"years" doc:"Newest first."`
	Reserve           *LoanReserve       `json:"reserve,omitempty" doc:"A reserve held against the mortgage that bears no interest (Air Bank's Chytrá rezerva)."`
}

type LoanReserve struct {
	AccountID        uuid.UUID    `json:"account_id"`
	Balance          money.Amount `json:"balance" doc:"Held today."`
	Saved            money.Amount `json:"saved" doc:"Interest saved on the instalments due by today."`
	Saves            money.Amount `json:"saves" doc:"Interest it will save on the projected instalments if it stays as it is."`
	PaidOffOn        *string      `json:"paid_off_on,omitempty" format:"date"`
	PaidOffWithoutOn *string      `json:"paid_off_without_on,omitempty" format:"date" doc:"When the loan would be repaid without the reserve."`
}

type reserveOutput struct {
	Body struct {
		AccountID uuid.UUID `json:"account_id" doc:"The reserve's account."`
	}
}

type overviewOutput struct {
	Body LoanOverview
}

type accountInput struct {
	ID uuid.UUID `path:"account_id"`
}

type scheduleOutput struct {
	Body struct {
		Instalments []LoanInstalment `json:"instalments" doc:"From the first terms to the last instalment, oldest first."`
	}
}

type saveTermsInput struct {
	ID        uuid.UUID `path:"account_id"`
	ValidFrom string    `path:"valid_from" format:"date"`
	Body      struct {
		BalanceMinor int64   `json:"balance_minor" minimum:"1" doc:"Owed on valid_from."`
		AnnualRate   string  `json:"annual_rate" pattern:"^[0-9]{1,2}(\\.[0-9]{1,4})?$" doc:"Percent a year." example:"4.89"`
		PaymentMinor int64   `json:"payment_minor" minimum:"1"`
		PaymentDay   int     `json:"payment_day" minimum:"1" maximum:"31"`
		MaturityOn   string  `json:"maturity_on" format:"date"`
		FixedUntil   *string `json:"fixed_until,omitempty" format:"date"`
	}
}

type deleteTermsInput struct {
	ID        uuid.UUID `path:"account_id"`
	ValidFrom string    `path:"valid_from" format:"date"`
}

type prepaymentInput struct {
	ID         uuid.UUID `path:"account_id"`
	ExtraMinor int64     `query:"extra_minor" minimum:"1" required:"true" doc:"The extra payment made today."`
}

type LoanRepayment struct {
	LoanPaymentRow money.Amount `json:"payment"`
	Months         int          `json:"months"`
	Interest       money.Amount `json:"interest"`
	PaidOffOn      string       `json:"paid_off_on" format:"date"`
}

type prepaymentOutput struct {
	Body struct {
		Extra        money.Amount  `json:"extra"`
		Current      LoanRepayment `json:"current" doc:"Without the extra payment."`
		KeepPayment  LoanRepayment `json:"keep_payment" doc:"The instalment stays, the loan ends sooner."`
		KeepMaturity LoanRepayment `json:"keep_maturity" doc:"The maturity stays, the instalment gets lower."`
	}
}

type markPaymentInput struct {
	TxID string `path:"tx_id" maxLength:"64"`
	Body struct {
		AccountID      uuid.UUID `json:"account_id" doc:"The mortgage or loan the payment pays."`
		InterestMinor  *int64    `json:"interest_minor,omitempty" minimum:"0" doc:"With principal_minor, the user's own split; both absent: split by the schedule."`
		PrincipalMinor *int64    `json:"principal_minor,omitempty" minimum:"0"`
	}
}

type unmarkPaymentInput struct {
	TxID string `path:"tx_id" maxLength:"64"`
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "get-loan", Method: http.MethodGet, Path: "/api/v1/loans/{account_id}", Tags: []string{"Loans"},
		Summary: "A mortgage or loan today: balance, terms, next instalment, payments and interest per year", Errors: []int{422},
	}}, h.overview)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "get-loan-schedule", Method: http.MethodGet, Path: "/api/v1/loans/{account_id}/schedule", Tags: []string{"Loans"},
		Summary: "Every instalment from the first terms to the last", Errors: []int{422},
	}}, h.schedule)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "save-loan-terms", Method: http.MethodPut, Path: "/api/v1/loans/{account_id}/terms/{valid_from}", Tags: []string{"Loans"},
		Summary:     "Set the terms in force from a day: the balance owed then, the rate and the instalment",
		Description: "A refixation is new terms from its day. Payments to the loan are split by the schedule again.",
		Errors:      []int{422},
	}}, h.saveTerms)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "delete-loan-terms", Method: http.MethodDelete, Path: "/api/v1/loans/{account_id}/terms/{valid_from}", Tags: []string{"Loans"},
		Summary: "Remove the terms from a day", DefaultStatus: http.StatusNoContent, Errors: []int{404, 422},
	}}, h.deleteTerms)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthRead, Scope: agentaccess.ScopeAccountsRead, Operation: huma.Operation{
		OperationID: "simulate-loan-prepayment", Method: http.MethodGet, Path: "/api/v1/loans/{account_id}/prepayment", Tags: []string{"Loans"},
		Summary: "What an extra payment today would save, keeping the instalment or the maturity", Errors: []int{422},
	}}, h.prepayment)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "enable-loan-reserve", Method: http.MethodPost, Path: "/api/v1/loans/{account_id}/reserve", Tags: []string{"Loans"},
		Summary: "Give a mortgage a reserve that lowers its interest, such as Air Bank's Chytrá rezerva",
		Description: "Creates the reserve's account at the mortgage's bank. Transfers into and out of it are recognised by the " +
			"bank's account number or operation type, and interest is charged on the balance less the reserve.",
		DefaultStatus: http.StatusCreated, Errors: []int{409, 422},
	}}, h.enableReserve)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "mark-loan-payment", Method: http.MethodPut, Path: "/api/v1/transactions/{tx_id}/loan-payment", Tags: []string{"Loans"},
		Summary:       "Record a transaction as an instalment of a mortgage or loan",
		Description:   "The interest becomes an expense, the principal a repayment; a transfer mark on the transaction is replaced.",
		DefaultStatus: http.StatusNoContent, Errors: []int{422},
	}}, h.markPayment)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "unmark-loan-payment", Method: http.MethodDelete, Path: "/api/v1/transactions/{tx_id}/loan-payment", Tags: []string{"Loans"},
		Summary:       "The transaction does not pay a loan",
		Description:   "Its categories are cleared and it will not be recognised as a payment to the loan again.",
		DefaultStatus: http.StatusNoContent, Errors: []int{404},
	}}, h.unmarkPayment)
}

func mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, loan.ErrNotALoan):
		return apierr.Unprocessable(apierr.CodeNotALoan)
	case errors.Is(err, loan.ErrTermsNotFound):
		return apierr.Unprocessable(apierr.CodeNoLoanTerms)
	case errors.Is(err, loan.ErrNotAPayment):
		return apierr.Unprocessable(apierr.CodeNotALoanPayment)
	case errors.Is(err, loan.ErrSplitMismatch):
		return apierr.Unprocessable(apierr.CodeLoanSplitMismatch)
	case errors.Is(err, loan.ErrExtraTooLarge):
		return apierr.Validation("query.extra_minor", "must be less than the balance owed")
	case errors.Is(err, loan.ErrReserveExists):
		return apierr.Conflict(apierr.CodeReserveExists)
	case errors.Is(err, loan.ErrPaymentMissing):
		return apierr.NotFound()
	default:
		return apierr.Internal(ctx, err)
	}
}

func toTerms(t loan.Terms, currency money.Currency) LoanTerms {
	return LoanTerms{ValidFrom: dto.Date(t.ValidFrom), Balance: money.New(t.BalanceMinor, currency), AnnualRate: t.AnnualRate.String(),
		LoanPaymentRow: money.New(t.PaymentMinor, currency), PaymentDay: t.PaymentDay, MaturityOn: dto.Date(t.MaturityOn),
		FixedUntil: dto.DatePtr(t.FixedUntil)}
}

func toInstalment(in loan.Instalment, currency money.Currency) LoanInstalment {
	status := "assumed"
	switch {
	case in.Paid:
		status = "paid"
	case in.Projected:
		status = "projected"
	}
	return LoanInstalment{DueOn: dto.Date(in.DueOn), Interest: money.New(in.InterestMinor, currency),
		Principal: money.New(in.PrincipalMinor, currency), Balance: money.New(in.BalanceMinor, currency), Status: status}
}

func (h *Handler) overview(ctx context.Context, in *accountInput) (*overviewOutput, error) {
	o, err := h.service.Overview(ctx, in.ID)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	c := o.Loan.Currency
	out := LoanOverview{AccountID: o.Loan.AccountID, Kind: string(o.Loan.Kind), Balance: money.New(o.BalanceMinor, c),
		Terms: make([]LoanTerms, len(o.Terms)), Payments: make([]LoanPaymentRow, 0, len(o.Payments)), Years: make([]LoanInterestYear, len(o.Years))}
	if o.Current != nil {
		current := toTerms(*o.Current, c)
		out.Current = &current
	}
	if o.Next != nil {
		next := toInstalment(*o.Next, c)
		out.Next = &next
	}
	var remainingInterest int64
	for _, i := range o.Schedule.Instalments {
		if i.Projected {
			out.RemainingMonths++
			remainingInterest += i.InterestMinor
		}
	}
	out.RemainingInterest = money.New(remainingInterest, c)
	if n := len(o.Schedule.Instalments); n > 0 {
		out.PaidOffOn = dto.DatePtr(&o.Schedule.Instalments[n-1].DueOn)
	}
	for i, t := range o.Terms {
		out.Terms[i] = toTerms(t, c)
	}
	for i := len(o.Payments) - 1; i >= 0; i-- {
		p := o.Payments[i]
		split := o.Schedule.Splits[p.TransactionID]
		out.Payments = append(out.Payments, LoanPaymentRow{TransactionID: p.TransactionID, BookedOn: dto.Date(p.BookedOn),
			Description: p.Description, Amount: money.New(p.AmountMinor, c), Interest: money.New(split.InterestMinor, c),
			Principal: money.New(split.PrincipalMinor, c), BySchedule: p.Fixed == nil})
	}
	if r := o.Reserve; r != nil {
		out.Reserve = &LoanReserve{AccountID: r.AccountID, Balance: money.New(r.BalanceMinor, c), Saved: money.New(r.SavedMinor, c),
			Saves: money.New(r.SavesMinor, c), PaidOffOn: dto.DatePtr(r.PaidOffOn), PaidOffWithoutOn: dto.DatePtr(r.PaidOffWithoutOn)}
	}
	for i, y := range o.Years {
		out.Years[i] = LoanInterestYear{Year: y.Year, Interest: money.New(y.InterestMinor, c), Deductible: money.New(y.DeductibleMinor, c)}
	}
	return &overviewOutput{Body: out}, nil
}

func (h *Handler) schedule(ctx context.Context, in *accountInput) (*scheduleOutput, error) {
	o, err := h.service.Overview(ctx, in.ID)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	out := &scheduleOutput{}
	out.Body.Instalments = make([]LoanInstalment, len(o.Schedule.Instalments))
	for i, in := range o.Schedule.Instalments {
		out.Body.Instalments[i] = toInstalment(in, o.Loan.Currency)
	}
	httpapi.CountResults(ctx, len(out.Body.Instalments))
	return out, nil
}

func parseDay(raw, location string) (time.Time, error) {
	day, err := dto.ParseDate(raw, location)
	if err != nil {
		return time.Time{}, err
	}
	if day == nil {
		return time.Time{}, apierr.Validation(location, "expected YYYY-MM-DD")
	}
	return *day, nil
}

func (h *Handler) saveTerms(ctx context.Context, in *saveTermsInput) (*struct{}, error) {
	validFrom, err := parseDay(in.ValidFrom, "path.valid_from")
	if err != nil {
		return nil, err
	}
	maturity, err := parseDay(in.Body.MaturityOn, "body.maturity_on")
	if err != nil {
		return nil, err
	}
	if !maturity.After(validFrom) {
		return nil, apierr.Validation("body.maturity_on", "must be after valid_from")
	}
	rate, err := decimal.NewFromString(in.Body.AnnualRate)
	if err != nil || rate.IsNegative() || rate.GreaterThanOrEqual(maxRate) {
		return nil, apierr.Validation("body.annual_rate", "expected a percentage below 100")
	}
	t := loan.Terms{ValidFrom: validFrom, BalanceMinor: in.Body.BalanceMinor, AnnualRate: rate, PaymentMinor: in.Body.PaymentMinor,
		PaymentDay: in.Body.PaymentDay, MaturityOn: maturity}
	if in.Body.FixedUntil != nil && *in.Body.FixedUntil != "" {
		fixed, err := parseDay(*in.Body.FixedUntil, "body.fixed_until")
		if err != nil {
			return nil, err
		}
		if !fixed.After(validFrom) {
			return nil, apierr.Validation("body.fixed_until", "must be after valid_from")
		}
		t.FixedUntil = &fixed
	}
	if err := h.service.SaveTerms(ctx, in.ID, t); err != nil {
		return nil, mapError(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) deleteTerms(ctx context.Context, in *deleteTermsInput) (*struct{}, error) {
	validFrom, err := parseDay(in.ValidFrom, "path.valid_from")
	if err != nil {
		return nil, err
	}
	err = h.service.DeleteTerms(ctx, in.ID, validFrom)
	if errors.Is(err, loan.ErrTermsNotFound) {
		return nil, apierr.NotFound()
	}
	if err != nil {
		return nil, mapError(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) prepayment(ctx context.Context, in *prepaymentInput) (*prepaymentOutput, error) {
	p, err := h.service.Prepayment(ctx, in.ID, in.ExtraMinor)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	o, err := h.service.Overview(ctx, in.ID)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	c := o.Loan.Currency
	repayment := func(r loan.Repayment) LoanRepayment {
		return LoanRepayment{LoanPaymentRow: money.New(r.PaymentMinor, c), Months: r.Months, Interest: money.New(r.InterestMinor, c),
			PaidOffOn: dto.Date(r.PaidOffOn)}
	}
	out := &prepaymentOutput{}
	out.Body.Extra = money.New(p.ExtraMinor, c)
	out.Body.Current, out.Body.KeepPayment, out.Body.KeepMaturity = repayment(p.Current), repayment(p.KeepPayment), repayment(p.KeepMaturity)
	return out, nil
}

func (h *Handler) markPayment(ctx context.Context, in *markPaymentInput) (*struct{}, error) {
	var fixed *loan.Split
	switch {
	case in.Body.InterestMinor != nil && in.Body.PrincipalMinor != nil:
		fixed = &loan.Split{InterestMinor: *in.Body.InterestMinor, PrincipalMinor: *in.Body.PrincipalMinor}
	case in.Body.InterestMinor != nil || in.Body.PrincipalMinor != nil:
		return nil, apierr.Validation("body", "give both interest_minor and principal_minor, or neither")
	}
	if err := h.service.MarkPayment(ctx, in.TxID, in.Body.AccountID, fixed); err != nil {
		return nil, mapError(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) unmarkPayment(ctx context.Context, in *unmarkPaymentInput) (*struct{}, error) {
	if err := h.service.UnmarkPayment(ctx, in.TxID); err != nil {
		return nil, mapError(ctx, err)
	}
	return &struct{}{}, nil
}

func (h *Handler) enableReserve(ctx context.Context, in *accountInput) (*reserveOutput, error) {
	out := &reserveOutput{}
	err := db.WithTx(ctx, h.pool, func(tx pgx.Tx) error {
		id, err := h.service.EnableReserve(ctx, tx, in.ID)
		if err != nil {
			return err
		}
		out.Body.AccountID = id
		// Detection also splits the loan's payments again, now on the balance less the reserve.
		if _, err := h.transfers.DetectOwn(ctx, tx, nil); err != nil {
			return err
		}
		return h.service.Reconcile(ctx, tx)
	})
	if err != nil {
		return nil, mapError(ctx, err)
	}
	return out, nil
}
