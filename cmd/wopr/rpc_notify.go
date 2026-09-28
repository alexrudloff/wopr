package main

// rpcNotification is a fire-and-forget notice for the RPC client, such as
// /llama progress.
type rpcNotification struct {
	Type       string `json:"type"`
	Message    string `json:"message"`
	NotifyType string `json:"notifyType,omitempty"`
}

// rpcNotifier returns a notify function that writes notices through output.
func rpcNotifier(output func(any)) func(message, kind string) {
	return func(message, kind string) {
		output(rpcNotification{Type: "ui_notify", Message: message, NotifyType: kind})
	}
}
