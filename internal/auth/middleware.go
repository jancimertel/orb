package auth

import (
	"log/slog"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
)

// AllowList returns a telego middleware that drops every update whose sender
// is not the configured operator. Allowed updates pass through untouched.
func AllowList(allowedUserID int64, logger *slog.Logger) th.Handler {
	return func(ctx *th.Context, update telego.Update) error {
		senderID, ok := senderID(update)
		if !ok {
			// No identifiable sender (e.g. channel post, edited business message
			// without `From`); drop silently.
			logger.Debug("auth: dropping update with no sender", "update_id", update.UpdateID)
			return nil
		}
		if senderID != allowedUserID {
			logger.Warn("auth: dropping update from unauthorized user",
				"update_id", update.UpdateID,
				"user_id", senderID,
			)
			return nil
		}
		return ctx.Next(update)
	}
}

func senderID(u telego.Update) (int64, bool) {
	switch {
	case u.Message != nil && u.Message.From != nil:
		return u.Message.From.ID, true
	case u.EditedMessage != nil && u.EditedMessage.From != nil:
		return u.EditedMessage.From.ID, true
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID, true
	}
	return 0, false
}
