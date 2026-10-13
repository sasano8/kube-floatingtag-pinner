package report_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/sasano8/kube-floatingtag-pinner/internal/report"
)

func capture(t *testing.T, level slog.Level) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level})))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestLevelsAreFixedPerKind(t *testing.T) {
	for e, want := range map[report.State]slog.Level{
		report.Match: slog.LevelInfo, report.NotRunning: slog.LevelInfo,
		report.Mismatch: slog.LevelWarn, report.NotPulled: slog.LevelWarn, report.Error: slog.LevelError,
	} {
		if e.Level() != want {
			t.Errorf("%s: %v want %v", e, e.Level(), want)
		}
	}
}

func TestStatesFilterAndLevelThreshold(t *testing.T) {
	buf := capture(t, slog.LevelInfo)
	r := report.Reporter{States: map[report.State]bool{report.Mismatch: true}}
	r.Emit(context.Background(), report.Match, "image", "a")
	r.Emit(context.Background(), report.Mismatch, "image", "b")
	if out := buf.String(); strings.Contains(out, "msg=match ") || !strings.Contains(out, "msg=mismatch") {
		t.Fatalf("filter: %q", out)
	}

	buf = capture(t, slog.LevelWarn) // --log-level=warn hides INFO states
	report.Reporter{}.Emit(context.Background(), report.Match)
	report.Reporter{}.Emit(context.Background(), report.NotPulled)
	if out := buf.String(); strings.Contains(out, "msg=match") || !strings.Contains(out, "msg=not-pulled") {
		t.Fatalf("level: %q", out)
	}
}

func TestParse(t *testing.T) {
	if _, err := report.ParseLevel("warn"); err != nil {
		t.Fatal(err)
	}
	if _, err := report.ParseLevel("loud"); err == nil {
		t.Fatal("unknown level must fail")
	}
	got, err := report.ParseStates("mismatch, error", report.Match, report.Mismatch, report.Error)
	if err != nil || !got[report.Mismatch] || !got[report.Error] || got[report.Match] {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := report.ParseStates("not-running", report.Match, report.Mismatch); err == nil {
		t.Fatal("a state the command cannot emit must fail")
	}
	if got, err := report.ParseStates("", report.Match); got != nil || err != nil {
		t.Fatalf("empty = every state: %v %v", got, err)
	}
}
