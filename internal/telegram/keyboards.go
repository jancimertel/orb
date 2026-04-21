package telegram

import "github.com/mymmrac/telego"

const (
	// Callback data format: "appr:<action>:<opID>".
	approvalPrefix      = "appr:"
	actionAllow         = "allow"
	actionAllowSession  = "allowses"
	actionDeny          = "deny"
	actionCancel        = "cancel"
)

// approvalKeyboard builds the inline keyboard shown alongside a Bash
// approval prompt.
func approvalKeyboard(opID string) *telego.InlineKeyboardMarkup {
	return &telego.InlineKeyboardMarkup{
		InlineKeyboard: [][]telego.InlineKeyboardButton{
			{
				{Text: "✅ Allow once", CallbackData: approvalPrefix + actionAllow + ":" + opID},
				{Text: "❌ Deny", CallbackData: approvalPrefix + actionDeny + ":" + opID},
			},
			{
				{Text: "🔓 Allow for session", CallbackData: approvalPrefix + actionAllowSession + ":" + opID},
			},
			{
				{Text: "🛑 Cancel turn", CallbackData: approvalPrefix + actionCancel + ":" + opID},
			},
		},
	}
}
