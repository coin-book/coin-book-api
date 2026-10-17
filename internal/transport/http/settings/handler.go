package settings

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"coin-book-api/internal/domain/categorization"
	"coin-book-api/internal/domain/settings"
	"coin-book-api/internal/transport/http/apierr"
	"coin-book-api/internal/transport/http/httpapi"
)

type Handler struct {
	repo        *settings.Repo
	categorizer *categorization.Service
}

func NewHandler(repo *settings.Repo, categorizer *categorization.Service) *Handler {
	return &Handler{repo: repo, categorizer: categorizer}
}

type Settings struct {
	AutoCategorizeThreshold float64 `json:"auto_categorize_threshold" minimum:"0" maximum:"1" doc:"Classifier confidence above which imports categorise on their own; 0 only suggests."`
	ClassifierExamples      int     `json:"classifier_examples" readOnly:"true" doc:"Categorised transactions the classifier currently learns from."`
}

type settingsOutput struct {
	Body Settings
}

type updateSettingsInput struct {
	Body struct {
		AutoCategorizeThreshold float64 `json:"auto_categorize_threshold" minimum:"0" maximum:"1"`
	}
}

func (h *Handler) Register(api huma.API) {
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "get-settings", Method: http.MethodGet, Path: "/api/v1/settings", Tags: []string{"Settings"},
		Summary: "Preferences and the state of the learned classifier",
	}}, h.get)
	httpapi.Register(api, httpapi.Op{Auth: httpapi.AuthSession, Operation: huma.Operation{
		OperationID: "update-settings", Method: http.MethodPut, Path: "/api/v1/settings", Tags: []string{"Settings"},
		Summary: "Change preferences", Errors: []int{422},
	}}, h.update)
}

func (h *Handler) get(ctx context.Context, _ *struct{}) (*settingsOutput, error) {
	return h.render(ctx)
}

func (h *Handler) update(ctx context.Context, in *updateSettingsInput) (*settingsOutput, error) {
	err := h.repo.SetThreshold(ctx, in.Body.AutoCategorizeThreshold)
	if errors.Is(err, settings.ErrInvalidThreshold) {
		return nil, apierr.Validation("body.auto_categorize_threshold", "0 (off) or between 0.5 and 1")
	}
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return h.render(ctx)
}

func (h *Handler) render(ctx context.Context) (*settingsOutput, error) {
	threshold, err := h.repo.Threshold(ctx, nil)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	model, err := h.categorizer.Model(ctx)
	if err != nil {
		return nil, apierr.Internal(ctx, err)
	}
	return &settingsOutput{Body: Settings{AutoCategorizeThreshold: threshold, ClassifierExamples: model.Examples()}}, nil
}
