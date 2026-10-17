// Package apierr is the API's error contract: RFC 9457 problem+json with a stable
// code the web client localizes (coin-book-web/packages/core/src/errors.ts).
package apierr

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	chimw "github.com/go-chi/chi/v5/middleware"
)

type Code string

const (
	CodeValidationFailed   Code = "validation_failed"
	CodeInvalidRequestBody Code = "invalid_request_body"
	CodeAuthRequired       Code = "auth_required"
	CodeForbidden          Code = "forbidden"
	CodeNotFound           Code = "not_found"
	CodeMethodNotAllowed   Code = "method_not_allowed"
	CodeRateLimited        Code = "rate_limited"
	CodeInternal           Code = "internal_error"
	CodeHostNotAllowed     Code = "host_not_allowed"

	CodeInvalidCredentials Code = "invalid_credentials" //nolint:gosec // G101: an error code
	CodePasswordTooShort   Code = "password_too_short"  //nolint:gosec // G101: an error code
	CodeEmailTaken         Code = "email_taken"
	CodeEmailNotVerified   Code = "email_not_verified"
	CodeGoogleSignInFailed Code = "google_sign_in_failed"
	CodeSessionRequired    Code = "session_required"
	CodeInsufficientScope  Code = "insufficient_scope"
	CodeUnknownScope       Code = "unknown_scope"
	CodeTokenTTLTooLong    Code = "token_ttl_too_long" //nolint:gosec // G101: an error code

	CodeUnsupportedFormat Code = "unsupported_format"
	CodeBalanceMismatch   Code = "balance_mismatch"
	CodeAlreadyImported   Code = "already_imported"
	CodeAlreadyRolledBack Code = "already_rolled_back"
	CodeFileTooLarge      Code = "file_too_large"
	CodeMappingRequired   Code = "mapping_required"
	CodeCurrencyMismatch  Code = "currency_mismatch"
	CodeMergeIntoItself   Code = "merge_into_itself"
	CodeAccountExists     Code = "account_exists"
	CodeUnknownBank       Code = "unknown_bank"

	CodeSplitMismatch  Code = "split_mismatch"
	CodeNothingToMatch Code = "nothing_to_match"
	CodeNotPairable    Code = "not_pairable"
	CodeNotPaired      Code = "not_paired"
	CodeInvalidParent  Code = "invalid_parent"
	CodeInvalidMerge   Code = "invalid_merge"
	CodeCategoryInUse  Code = "category_in_use"
	CodeSystemCategory Code = "system_category"
	CodeNotOwnAccount  Code = "not_own_account"

	CodeInvalidBudgetLine   Code = "invalid_budget_line"
	CodeDuplicateBudgetLine Code = "duplicate_budget_line"
	CodeInvalidCurrency     Code = "invalid_currency"
	CodeInvalidCursor       Code = "invalid_cursor"
)

var details = map[Code]string{ //nolint:gosec // G101: error descriptions, not credentials
	CodeValidationFailed:    "The request did not pass validation; see errors for the offending fields.",
	CodeInvalidRequestBody:  "The request body could not be parsed.",
	CodeAuthRequired:        "Authentication is required.",
	CodeForbidden:           "You do not have access to this resource.",
	CodeNotFound:            "The resource does not exist.",
	CodeMethodNotAllowed:    "That method is not allowed on this path.",
	CodeRateLimited:         "Too many requests; slow down and retry later.",
	CodeInternal:            "The request could not be completed.",
	CodeHostNotAllowed:      "Requests must address the API by an allowed host name.",
	CodeInvalidCredentials:  "Incorrect email or password.",
	CodePasswordTooShort:    "The password is too short.",
	CodeEmailTaken:          "A user with this email already exists.",
	CodeEmailNotVerified:    "The sign-in provider has not verified this email address.",
	CodeGoogleSignInFailed:  "Signing in with Google did not complete; try again.",
	CodeSessionRequired:     "This operation needs a signed-in session; an agent token is not accepted.",
	CodeInsufficientScope:   "The agent token does not carry the scope this operation needs.",
	CodeUnknownScope:        "That is not a known scope.",
	CodeTokenTTLTooLong:     "That expiry is further out than an agent token may live.",
	CodeUnsupportedFormat:   "The file is not an export this application can read.",
	CodeBalanceMismatch:     "The statement does not match its own control totals; import with force to accept it anyway.",
	CodeAlreadyImported:     "This file has already been imported.",
	CodeAlreadyRolledBack:   "This import has already been rolled back.",
	CodeFileTooLarge:        "The file exceeds the maximum upload size.",
	CodeMappingRequired:     "The file does not say which account it belongs to; choose one in the mapping.",
	CodeCurrencyMismatch:    "The accounts hold different currencies.",
	CodeMergeIntoItself:     "An account cannot be merged into itself.",
	CodeAccountExists:       "An account with this number already exists.",
	CodeUnknownBank:         "That bank is not in the register.",
	CodeSplitMismatch:       "The category lines do not add up to the transaction amount.",
	CodeNothingToMatch:      "The transaction has no value a rule could match on.",
	CodeNotPairable:         "These transactions cannot form a transfer.",
	CodeNotPaired:           "The transaction is not part of a transfer.",
	CodeInvalidParent:       "A subcategory must belong under a main category.",
	CodeInvalidMerge:        "A category cannot be merged into itself or its own subcategory.",
	CodeCategoryInUse:       "The category is in use; choose a category to merge it into.",
	CodeNotOwnAccount:       "Choose another own account than the one the transaction belongs to.",
	CodeSystemCategory:      "A system category keeps its name, kind and place; only its colour, icon and group can change.",
	CodeInvalidBudgetLine:   "A budget line needs exactly one category or group and a non-negative amount.",
	CodeDuplicateBudgetLine: "The same category or group is budgeted twice.",
	CodeInvalidCurrency:     "That is not an ISO 4217 currency code.",
	CodeInvalidCursor:       "The pagination cursor is not valid.",
}

func Codes() []Code {
	out := make([]Code, 0, len(details))
	for c := range details {
		out = append(out, c)
	}
	return out
}

func (Code) Schema(huma.Registry) *huma.Schema {
	values := []any{}
	for _, c := range sortedCodes() {
		values = append(values, string(c))
	}
	return &huma.Schema{Type: huma.TypeString, Title: "ErrorCode", Enum: values,
		Description: "Stable machine-readable error identifier. Branch on this, not on `detail`."}
}

type Problem struct {
	Title    string              `json:"title"`
	Status   int                 `json:"status"`
	Code     Code                `json:"code"`
	Detail   string              `json:"detail,omitempty" doc:"English explanation; localize from code instead of showing this."`
	Instance string              `json:"instance,omitempty" doc:"Request id of this occurrence."`
	Errors   []*huma.ErrorDetail `json:"errors,omitempty"`
}

func (p *Problem) Error() string { return p.Detail }

func (p *Problem) GetStatus() int { return p.Status }

func (p *Problem) ContentType(ct string) string {
	if ct == "application/json" {
		return "application/problem+json"
	}
	return ct
}

// Install routes Huma's own errors (validation, unparseable bodies) through Problem.
func Install() {
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		p := New(status, codeForStatus(status))
		if msg != "" {
			p.Detail = msg
		}
		for _, err := range errs {
			if err == nil {
				continue
			}
			if d, ok := err.(huma.ErrorDetailer); ok {
				p.Errors = append(p.Errors, d.ErrorDetail())
				continue
			}
			p.Errors = append(p.Errors, &huma.ErrorDetail{Message: err.Error()})
		}
		return p
	}
}

func codeForStatus(status int) Code {
	switch status {
	case http.StatusBadRequest:
		return CodeInvalidRequestBody
	case http.StatusUnauthorized:
		return CodeAuthRequired
	case http.StatusForbidden:
		return CodeForbidden
	case http.StatusNotFound:
		return CodeNotFound
	case http.StatusMethodNotAllowed:
		return CodeMethodNotAllowed
	case http.StatusRequestEntityTooLarge:
		return CodeFileTooLarge
	case http.StatusUnprocessableEntity:
		return CodeValidationFailed
	case http.StatusTooManyRequests:
		return CodeRateLimited
	default:
		return CodeInternal
	}
}

func New(status int, code Code) *Problem {
	return &Problem{Title: http.StatusText(status), Status: status, Code: code, Detail: details[code]}
}

func BadRequest(code Code) *Problem    { return New(http.StatusBadRequest, code) }
func Unauthorized() *Problem           { return New(http.StatusUnauthorized, CodeAuthRequired) }
func Forbidden(code Code) *Problem     { return New(http.StatusForbidden, code) }
func NotFound() *Problem               { return New(http.StatusNotFound, CodeNotFound) }
func Conflict(code Code) *Problem      { return New(http.StatusConflict, code) }
func Unprocessable(code Code) *Problem { return New(http.StatusUnprocessableEntity, code) }

func Validation(location, message string) *Problem {
	p := New(http.StatusUnprocessableEntity, CodeValidationFailed)
	p.Errors = []*huma.ErrorDetail{{Location: location, Message: message}}
	return p
}

func Internal(ctx context.Context, err error) *Problem {
	reqID := chimw.GetReqID(ctx)
	slog.ErrorContext(ctx, "request failed", "err", err, "request_id", reqID)
	p := New(http.StatusInternalServerError, CodeInternal)
	p.Instance = reqID
	return p
}

func Write(w http.ResponseWriter, p *Problem) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(p.Status)
	if err := json.NewEncoder(w).Encode(p); err != nil {
		slog.Error("write error response", "err", err)
	}
}
