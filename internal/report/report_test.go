package report_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/sasano8/pinner/internal/report"
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

func TestLinesStartWithTheStage(t *testing.T) {
	buf := capture(t, slog.LevelInfo)
	report.Reporter{Stage: "pull"}.Emit(context.Background(), report.Mismatch)
	report.Reporter{Stage: "trigger"}.Emit(context.Background(), report.Match)
	out := buf.String()
	if !strings.Contains(out, `msg="pull mismatch"`) || !strings.Contains(out, `msg="trigger match"`) {
		t.Fatalf("%q", out)
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

func TestShortenDigests(t *testing.T) {
	const full = "sha256:bf16c8e0676280afbe3455829ceb176a47bdb23140a7ece99fba4b0e1e899fd1"
	const other = "sha256:4a45bcf8c937c3931fc6fa881fa9b4bcbea3373aee6eda35b6a03a08efa6099b"

	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: report.ShortenDigests(12)}))
	log.Info("pull match", "remote", full, "local", []string{full, other},
		"running", "revision="+other, "err", errors.New("pull of "+full+" failed"), "image", "ghcr.io/o/app:latest", "n", 3)
	out := buf.String()
	for _, want := range []string{
		"remote=sha256:bf16c8e06762...",
		"sha256:4a45bcf8c937...",
		`running="revision=sha256:4a45bcf8c937..."`,
		"pull of sha256:bf16c8e06762... failed",
		"image=ghcr.io/o/app:latest", "n=3",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if strings.Contains(out, "676280afbe34") {
		t.Errorf("the tail of a digest must be cut: %s", out)
	}

	// 0 = whole; a length of the whole digest or more changes nothing
	if report.ShortenDigests(0) != nil {
		t.Error("0 must not shorten")
	}
	buf.Reset()
	slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{ReplaceAttr: report.ShortenDigests(64)})).Info("x", "d", full)
	if !strings.Contains(buf.String(), full) {
		t.Errorf("a full-length setting must keep the digest: %s", buf.String())
	}
}
