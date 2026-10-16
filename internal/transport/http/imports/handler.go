package imports

import (
	"context"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/google/uuid"

	"coin-book-api/internal/domain/importing"
	"coin-book-api/internal/platform/money"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/dto"
	"coin-book-api/internal/transport/http/httpapi"
)

type Handler struct {
	service        *importing.Service
	maxUploadBytes int64
}

func NewHandler(service *importing.Service, maxUploadBytes int64) *Handler {
	return &Handler{service: service, maxUploadBytes: maxUploadBytes}
}

type uploadForm struct {
	File huma.FormFile `form:"file" contentType:"text/csv,text/plain,application/octet-stream" required:"true"`
}

type uploadInput struct {
	Force   bool `query:"force" doc:"Write the import even though its control totals do not match (F-IMP-3)."`
	RawBody huma.MultipartFormFiles[uploadForm]
}

type previewInput struct {
	RawBody huma.MultipartFormFiles[uploadForm]
}

type ImportCheck struct {
	Kind          string `json:"kind" enum:"totals,balance_chain,declared_sum"`
	OK            bool   `json:"ok"`
	ExpectedMinor int64  `json:"expected_minor"`
	ActualMinor   int64  `json:"actual_minor"`
	Line          int    `json:"line,omitempty"`
}

type ImportAccount struct {
	Identity   string        `json:"identity" doc:"Bank account number or importer reference."`
	Name       string        `json:"name"`
	Currency   string        `json:"currency"`
	AccountID  *uuid.UUID    `json:"account_id,omitempty"`
	IsNew      bool          `json:"is_new" doc:"The import will create this account."`
	Rows       int           `json:"rows"`
	New        int           `json:"new"`
	Duplicates int           `json:"duplicates"`
	Checks     []ImportCheck `json:"checks"`
}

type ImportPreviewRow struct {
	AccountIdentity string       `json:"account_identity"`
	Line            int          `json:"line"`
	BookedOn        string       `json:"booked_on" format:"date"`
	Amount          money.Amount `json:"amount"`
	Description     string       `json:"description"`
	Status          string       `json:"status" enum:"new,duplicate"`
}

type ImportResult struct {
	BatchID          *uuid.UUID         `json:"batch_id,omitempty" doc:"Set once the import is written."`
	FileName         string             `json:"file_name"`
	Format           string             `json:"format"`
	PeriodFrom       *string            `json:"period_from,omitempty" format:"date"`
	PeriodTo         *string            `json:"period_to,omitempty" format:"date"`
	AlreadyImported  bool               `json:"already_imported"`
	ChecksPassed     bool               `json:"checks_passed"`
	Accounts         []ImportAccount    `json:"accounts"`
	Rows             []ImportPreviewRow `json:"rows" doc:"The first 500 rows with their status."`
	RowsTotal        int                `json:"rows_total"`
	RowsNew          int                `json:"rows_new"`
	RowsDuplicate    int                `json:"rows_duplicate"`
	CategorizedRule  int                `json:"categorized_by_rule"`
	CategorizedBank  int                `json:"categorized_by_bank"`
	TransfersMatched int                `json:"transfers_matched"`
}

type resultOutput struct {
	Body ImportResult
}

type ImportBatch struct {
	ID            uuid.UUID  `json:"id"`
	FileName      string     `json:"file_name"`
	Format        string     `json:"format"`
	PeriodFrom    *string    `json:"period_from,omitempty" format:"date"`
	PeriodTo      *string    `json:"period_to,omitempty" format:"date"`
	Status        string     `json:"status" enum:"committed,rolled_back"`
	Forced        bool       `json:"forced"`
	RowsTotal     int        `json:"rows_total"`
	RowsInserted  int        `json:"rows_inserted"`
	RowsDuplicate int        `json:"rows_duplicate"`
	CreatedAt     time.Time  `json:"created_at"`
	RolledBackAt  *time.Time `json:"rolled_back_at,omitempty"`
}

type listImportsOutput struct {
	Body struct {
		Imports []ImportBatch `json:"imports"`
	}
}

type rollbackInput struct {
	ID uuid.UUID `path:"id"`
}

type rollbackOutput struct {
	Body struct {
		Removed int `json:"removed"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "preview-import", Method: http.MethodPost, Path: "/api/v1/imports/preview", Tags: []string{"Imports"},
		Summary: "Dry-run an import: parse, check and deduplicate without writing", MaxBodyBytes: h.maxUploadBytes,
		Errors: []int{413, 422},
	}}, h.preview)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "commit-import", Method: http.MethodPost, Path: "/api/v1/imports", Tags: []string{"Imports"},
		Summary: "Import a statement atomically", MaxBodyBytes: h.maxUploadBytes, DefaultStatus: http.StatusCreated,
		Description: "Answers `409 balance_mismatch` when the statement disagrees with its own control totals " +
			"(repeat with `force=true` to accept), and `409 already_imported` for a file imported before.",
		Errors: []int{409, 413, 422},
	}}, h.commit)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "list-imports", Method: http.MethodGet, Path: "/api/v1/imports", Tags: []string{"Imports"},
		Summary: "Import history",
	}}, h.list)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "rollback-import", Method: http.MethodPost, Path: "/api/v1/imports/{id}/rollback", Tags: []string{"Imports"},
		Summary: "Remove every transaction an import created", Errors: []int{404, 409},
	}}, h.rollback)
}

func (h *Handler) preview(ctx context.Context, in *previewInput) (*resultOutput, error) {
	name, content, err := readFile(in.RawBody.Data().File)
	if err != nil {
		return nil, err
	}
	result, err := h.service.Preview(ctx, name, content)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	return &resultOutput{Body: toResult(result)}, nil
}

func (h *Handler) commit(ctx context.Context, in *uploadInput) (*resultOutput, error) {
	name, content, err := readFile(in.RawBody.Data().File)
	if err != nil {
		return nil, err
	}
	result, err := h.service.Commit(ctx, name, content, in.Force)
	if err != nil {
		return nil, mapError(ctx, err)
	}
	return &resultOutput{Body: toResult(result)}, nil
}

func (h *Handler) list(ctx context.Context, _ *struct{}) (*listImportsOutput, error) {
	batches, err := h.service.List(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	out := &listImportsOutput{}
	out.Body.Imports = make([]ImportBatch, len(batches))
	for i, b := range batches {
		out.Body.Imports[i] = ImportBatch{ID: b.ID, FileName: b.FileName, Format: b.Format, PeriodFrom: dto.DatePtr(b.PeriodFrom),
			PeriodTo: dto.DatePtr(b.PeriodTo), Status: b.Status, Forced: b.Forced, RowsTotal: b.RowsTotal,
			RowsInserted: b.RowsInserted, RowsDuplicate: b.RowsDuplicate, CreatedAt: b.CreatedAt, RolledBackAt: b.RolledBackAt}
	}
	return out, nil
}

func (h *Handler) rollback(ctx context.Context, in *rollbackInput) (*rollbackOutput, error) {
	removed, err := h.service.Rollback(ctx, in.ID)
	switch {
	case errors.Is(err, importing.ErrBatchNotFound):
		return nil, apierr.NotFound()
	case errors.Is(err, importing.ErrAlreadyRolledBack):
		return nil, apierr.Conflict(apierr.CodeAlreadyRolledBack)
	case err != nil:
		return nil, apierr.Internal(ctx, err)
	}
	out := &rollbackOutput{}
	out.Body.Removed = removed
	return out, nil
}

func readFile(f huma.FormFile) (string, []byte, error) {
	defer func() { _ = f.Close() }()
	content, err := io.ReadAll(f)
	if err != nil {
		return "", nil, apierr.BadRequest(apierr.CodeInvalidRequestBody)
	}
	return f.Filename, content, nil
}

func mapError(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, importing.ErrUnsupportedFormat):
		return apierr.Unprocessable(apierr.CodeUnsupportedFormat)
	case errors.Is(err, importing.ErrChecksFailed):
		return apierr.Conflict(apierr.CodeBalanceMismatch)
	case errors.Is(err, importing.ErrAlreadyImported):
		return apierr.Conflict(apierr.CodeAlreadyImported)
	default:
		return apierr.Internal(ctx, err)
	}
}

func toResult(r *importing.Result) ImportResult {
	out := ImportResult{
		BatchID: r.BatchID, FileName: r.FileName, Format: string(r.Format), PeriodFrom: dto.DatePtr(r.PeriodFrom),
		PeriodTo: dto.DatePtr(r.PeriodTo), AlreadyImported: r.AlreadyImported, ChecksPassed: r.ChecksPassed,
		RowsTotal: r.RowsTotal, RowsNew: r.RowsNew, RowsDuplicate: r.RowsDuplicate, CategorizedRule: r.CategorizedRule,
		CategorizedBank: r.CategorizedBank, TransfersMatched: r.TransfersMatched,
		Accounts: make([]ImportAccount, len(r.Accounts)), Rows: make([]ImportPreviewRow, len(r.Rows)),
	}
	for i, a := range r.Accounts {
		checks := make([]ImportCheck, len(a.Checks))
		for j, c := range a.Checks {
			checks[j] = ImportCheck{Kind: string(c.Kind), OK: c.OK, ExpectedMinor: c.ExpectedMinor, ActualMinor: c.ActualMinor, Line: c.Line}
		}
		out.Accounts[i] = ImportAccount{Identity: a.Ref.Identity(), Name: a.Ref.Name, Currency: string(a.Ref.Currency),
			AccountID: a.AccountID, IsNew: a.IsNew, Rows: a.Rows, New: a.New, Duplicates: a.Duplicates, Checks: checks}
	}
	for i, row := range r.Rows {
		out.Rows[i] = ImportPreviewRow{AccountIdentity: row.AccountRef, Line: row.Line, BookedOn: dto.Date(row.BookedOn),
			Amount: row.Amount, Description: row.Description, Status: string(row.Status)}
	}
	return out
}
