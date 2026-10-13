// Package logging monta o logger slog do processo.
package logging

import (
	"io"
	"log/slog"

	"github.com/brianashihara/coreledger/internal/platform/config"
)

// New devolve um logger texto para desenvolvimento local ou um logger JSON
// para máquinas (LOG_FORMAT=json).
func New(w io.Writer, cfg config.Log) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.Level}
	if cfg.Format == "json" {
		return slog.New(slog.NewJSONHandler(w, opts))
	}
	return slog.New(slog.NewTextHandler(w, opts))
}
