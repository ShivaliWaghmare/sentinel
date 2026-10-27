package agents

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/ShivaliWaghmare/sentinel/server/internal/sse"
	"github.com/ShivaliWaghmare/sentinel/server/internal/store"
)

// maxLineBytes is the longest event line accepted. Large payloads belong in
// files (the protocol puts paths in events), so a longer line is dropped
// with a warning.
const maxLineBytes = 1 << 20

// tailBytes is how much of agent.log goes into an error message.
const tailBytes = 4 << 10

// eventTypes are the types of the agent event protocol (ARCHITECTURE.md).
var eventTypes = map[string]bool{
	"run_started": true, "phase": true, "llm_call": true, "thought": true,
	"tool_call": true, "tool_result": true, "artifact": true,
	"test_validation": true, "warning": true, "done": true,
}

// line is one event as the CLI writes it.
type line struct {
	Seq  *int64          `json:"seq"`
	TS   string          `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// doneData is the part of a done event the run record needs.
type doneData struct {
	Status  string
	Summary string // JSON object
	Error   string
}

// recorder stores and publishes one run's events. It owns the run's seq:
// the server numbers events itself, so warnings it adds (for a malformed
// line) and a done it writes (for a crashed agent) fit into the sequence.
// The CLI's own seq is only checked for order.
type recorder struct {
	s          *Supervisor
	ctx        context.Context
	runID      int64
	seq        int64
	lastCLISeq int64
	done       *doneData       // the CLI's done event, once seen
	finished   *store.AgentRun // the run as finished by the stored done event
}

func newRecorder(s *Supervisor, ctx context.Context, runID int64) *recorder {
	return &recorder{s: s, ctx: ctx, runID: runID}
}

// readEvents reads JSONL from r until EOF and hands each line to rec.
func readEvents(r io.Reader, rec *recorder, logger *slog.Logger) {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		b, tooLong, err := readLine(br, maxLineBytes)
		switch {
		case tooLong:
			rec.warn(fmt.Sprintf("event line over %d bytes dropped", maxLineBytes))
		case len(bytes.TrimSpace(b)) > 0:
			rec.handleLine(b)
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, os.ErrClosed) {
				logger.Error("read agent stdout", "agent_run_id", rec.runID, "err", err)
			}
			return
		}
	}
}

// readLine returns the next line without its newline. A line longer than
// max is consumed and reported as tooLong, so reading continues after it.
func readLine(br *bufio.Reader, max int) (b []byte, tooLong bool, err error) {
	for {
		chunk, isPrefix, err := br.ReadLine()
		if !tooLong {
			if len(b)+len(chunk) > max {
				tooLong, b = true, nil
			} else {
				b = append(b, chunk...)
			}
		}
		if err != nil || !isPrefix {
			return b, tooLong, err
		}
	}
}

// handleLine validates one line and records it, or records a warning.
func (r *recorder) handleLine(b []byte) {
	var ev line
	if err := json.Unmarshal(b, &ev); err != nil {
		r.warn(fmt.Sprintf("invalid event line (not JSON): %s", preview(b)))
		return
	}
	switch {
	case ev.Seq == nil:
		r.warn(fmt.Sprintf("invalid event line (no seq): %s", preview(b)))
		return
	case !eventTypes[ev.Type]:
		r.warn(fmt.Sprintf("invalid event line (unknown type %q): %s", ev.Type, preview(b)))
		return
	case !isObject(ev.Data):
		r.warn(fmt.Sprintf("invalid event line (data is not an object): %s", preview(b)))
		return
	}
	if r.done != nil {
		r.warn(fmt.Sprintf("event after done ignored: %s", ev.Type))
		return
	}
	if *ev.Seq <= r.lastCLISeq {
		r.warn(fmt.Sprintf("agent event seq %d after %d (out of order)", *ev.Seq, r.lastCLISeq))
	} else {
		r.lastCLISeq = *ev.Seq
	}

	var usage store.Usage
	switch ev.Type {
	case "llm_call":
		var d struct {
			TokensIn  int64   `json:"tokens_in"`
			TokensOut int64   `json:"tokens_out"`
			CostUSD   float64 `json:"cost_usd"`
		}
		if err := json.Unmarshal(ev.Data, &d); err != nil {
			r.warn(fmt.Sprintf("llm_call with invalid usage: %v", err))
		}
		usage = store.Usage{Steps: 1, TokensIn: d.TokensIn, TokensOut: d.TokensOut, CostUSD: d.CostUSD}
	case "done":
		done, err := parseDone(ev.Data)
		if err != nil {
			r.warn(fmt.Sprintf("invalid done event ignored: %v", err))
			return
		}
		r.done = &done
		r.finish(ev.Data, done)
		return
	}
	r.record(ev.Type, ev.Data, usage)
}

func parseDone(data json.RawMessage) (doneData, error) {
	var d struct {
		Status  string          `json:"status"`
		Summary json.RawMessage `json:"summary"`
		Error   string          `json:"error"`
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return doneData{}, err
	}
	if !store.FinalAgentStatus(d.Status) {
		return doneData{}, fmt.Errorf("status %q is not a final status", d.Status)
	}
	summary := "{}"
	if isObject(d.Summary) {
		summary = string(d.Summary)
	}
	return doneData{Status: d.Status, Summary: summary, Error: d.Error}, nil
}

// synthesizeDone records a done event the CLI never wrote (it crashed, was
// killed, or never started), so SSE clients always see the end of a run.
func (r *recorder) synthesizeDone(status, errMsg string) {
	data, _ := json.Marshal(map[string]any{"status": status, "summary": map[string]any{}, "error": errMsg})
	r.finish(data, doneData{Status: status, Summary: "{}", Error: errMsg})
}

// finish stores a done event together with the run's final state, then
// publishes it: an SSE client that sees done can read the finished run.
func (r *recorder) finish(data json.RawMessage, d doneData) {
	r.seq++
	ev := store.AgentEvent{AgentRunID: r.runID, Seq: r.seq, Type: "done", PayloadJSON: string(data), CreatedAt: time.Now().UTC()}
	run, err := r.s.store.AppendDoneEvent(r.ctx, ev, d.Status, d.Summary, d.Error)
	if err != nil {
		r.s.logger.Error("store done event", "agent_run_id", r.runID, "err", err)
		return
	}
	r.finished = &run
	r.s.broker.Publish(r.runID, sse.Event{Seq: ev.Seq, TS: ev.CreatedAt, Type: "done", Data: data})
}

func (r *recorder) warn(msg string) {
	data, _ := json.Marshal(map[string]string{"message": msg})
	r.record("warning", data, store.Usage{})
}

// record stores one event (with its usage) and publishes it.
func (r *recorder) record(typ string, data json.RawMessage, usage store.Usage) {
	r.seq++
	ev := store.AgentEvent{
		AgentRunID: r.runID, Seq: r.seq, Type: typ, PayloadJSON: string(data), CreatedAt: time.Now().UTC(),
	}
	if err := r.s.store.AppendAgentEvent(r.ctx, ev, usage); err != nil {
		r.s.logger.Error("store agent event", "agent_run_id", r.runID, "type", typ, "err", err)
		return
	}
	r.s.broker.Publish(r.runID, sse.Event{Seq: ev.Seq, TS: ev.CreatedAt, Type: typ, Data: data})
}

func isObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) > 0 && t[0] == '{'
}

// preview shortens a bad line for a warning message.
func preview(b []byte) string {
	const max = 200
	s := string(b)
	if len(s) > max {
		s = s[:max]
		for !utf8.ValidString(s) {
			s = s[:len(s)-1]
		}
		s += "…"
	}
	return s
}

// tailFile returns the last tailBytes of the file at path, or a note if it
// is empty or can't be read.
func tailFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "(no output)"
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > tailBytes {
		if _, err := f.Seek(-tailBytes, io.SeekEnd); err != nil {
			return "(no output)"
		}
	}
	b, err := io.ReadAll(f)
	if err != nil || len(bytes.TrimSpace(b)) == 0 {
		return "(no output)"
	}
	return strings.TrimSpace(string(b))
}
