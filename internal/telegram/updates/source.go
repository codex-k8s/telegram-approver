package updates

import (
	"context"
	"net/http"

	"github.com/mymmrac/telego"
)

// Source provides Telegram updates.
type Source interface {
	// Start begins updates processing.
	Start(ctx context.Context) error
	// Stop stops updates processing.
	Stop(ctx context.Context) error
	// Updates returns the updates channel.
	Updates() <-chan telego.Update
	// Handler returns HTTP handler for webhook mode (nil for long polling).
	Handler() http.Handler
}
