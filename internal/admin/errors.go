package admin

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"mycdn/internal/analytics"
	"mycdn/internal/cache"
	"mycdn/internal/rules"
)

func fieldError(path, message string) error {
	return huma.Error422UnprocessableEntity("validation failed", &huma.ErrorDetail{Location: path, Message: message})
}

func managementError(operation string, err error) error {
	var configError *rules.ValidationError
	if errors.As(err, &configError) {
		return fieldError(configError.Path, configError.Message)
	}
	var queryError *analytics.ValidationError
	if errors.As(err, &queryError) {
		return fieldError(queryError.Field, queryError.Message)
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return huma.Error504GatewayTimeout(operation + " timed out")
	case errors.Is(err, context.Canceled):
		return huma.NewError(http.StatusRequestTimeout, operation+" canceled")
	case errors.Is(err, analytics.ErrDetailRetention):
		return fieldError("from", err.Error())
	case errors.Is(err, analytics.ErrBusy), errors.Is(err, cache.ErrBusy):
		return huma.Error503ServiceUnavailable(operation + ": concurrency limit reached")
	case errors.Is(err, analytics.ErrStoragePressure), errors.Is(err, cache.ErrCapacity):
		return huma.Error503ServiceUnavailable(operation + ": storage budget exhausted")
	case errors.Is(err, analytics.ErrClosed), errors.Is(err, cache.ErrClosed):
		return huma.Error503ServiceUnavailable(operation + ": store is closed")
	default:
		return huma.Error500InternalServerError(operation+" failed", err)
	}
}
