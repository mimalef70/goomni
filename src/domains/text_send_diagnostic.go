package domains

import "context"

type textSendDiagnosticKey struct{}

// TextSendDiagnostic belongs to one synchronous Send invocation. Adapters fill
// it on the calling goroutine, using protocol constants and numeric codes only.
type TextSendDiagnostic struct {
	Stage         string
	HTTPStatus    int
	ProviderCode  int64
	Reason        string
	Renewal       string
	PeerHash      string
	RejectedField string
}

func WithTextSendDiagnostic(ctx context.Context, d *TextSendDiagnostic) context.Context {
	return context.WithValue(ctx, textSendDiagnosticKey{}, d)
}

func TextSendDiagnosticFrom(ctx context.Context) *TextSendDiagnostic {
	d, _ := ctx.Value(textSendDiagnosticKey{}).(*TextSendDiagnostic)
	return d
}
