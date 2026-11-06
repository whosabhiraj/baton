package redact

import (
	"context"
	"log/slog"
)

// handler wraps a slog.Handler and redacts the message and all attribute values.
type handler struct{ inner slog.Handler }

// NewHandler returns a slog.Handler that redacts secrets in log messages and attributes
// before passing records to inner.
func NewHandler(inner slog.Handler) slog.Handler { return handler{inner} }

func (h handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h handler) Handle(ctx context.Context, rec slog.Record) error {
	out := slog.NewRecord(rec.Time, rec.Level, String(rec.Message), rec.PC)
	rec.Attrs(func(a slog.Attr) bool {
		out.AddAttrs(redactAttr(a))
		return true
	})
	return h.inner.Handle(ctx, out)
}

func (h handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	red := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		red[i] = redactAttr(a)
	}
	return handler{h.inner.WithAttrs(red)}
}

func (h handler) WithGroup(name string) slog.Handler { return handler{h.inner.WithGroup(name)} }

func redactAttr(a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	switch a.Value.Kind() {
	case slog.KindString:
		if sensitiveKey.MatchString(a.Key) {
			return slog.String(a.Key, r)
		}
		return slog.String(a.Key, String(a.Value.String()))
	case slog.KindGroup:
		g := a.Value.Group()
		out := make([]slog.Attr, len(g))
		for i, ga := range g {
			out[i] = redactAttr(ga)
		}
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(out...)}
	case slog.KindAny:
		if s, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, String(s.Error()))
		}
		return slog.Any(a.Key, Value(a.Value.Any()))
	default:
		return a
	}
}
