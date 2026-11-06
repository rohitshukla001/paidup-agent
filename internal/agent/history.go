package agent

import (
	"context"
	"encoding/json"

	"paidup-agent/internal/contract"
	"paidup-agent/internal/domain"
)

// Stored message formats. A user message keeps plain text. A model
// message and a tool message keep JSON, so function calls, their thought
// signatures, and their results survive a reload.

type storedCall struct {
	Name      string         `json:"name"`
	Args      map[string]any `json:"args,omitempty"`
	Signature []byte         `json:"signature,omitempty"`
}

type storedModel struct {
	Text  string       `json:"text,omitempty"`
	Calls []storedCall `json:"calls,omitempty"`
}

type storedResult struct {
	Name   string         `json:"name"`
	Output map[string]any `json:"output"`
}

type storedTool struct {
	Results []storedResult `json:"results"`
}

// saveTurn stores one model turn or one function-result turn and returns
// the message ID.
func (s *Service) saveTurn(ctx context.Context, sessionID string,
	t contract.Turn) string {
	m := domain.Message{ID: domain.NewID(), SessionID: sessionID,
		CreatedAt: s.opt.Now()}
	var payload any
	if t.Role == "model" {
		sm := storedModel{Text: t.Text}
		for _, c := range t.Calls {
			sm.Calls = append(sm.Calls, storedCall{Name: c.Name, Args: c.Args,
				Signature: c.Signature})
		}
		m.Role, payload = "model", sm
	} else {
		st := storedTool{}
		for _, r := range t.Results {
			st.Results = append(st.Results, storedResult{Name: r.Name,
				Output: r.Output})
		}
		m.Role, payload = "tool", st
	}
	b, _ := json.Marshal(payload)
	m.Content = string(b)
	if err := s.store.AddMessage(ctx, m); err != nil {
		s.opt.Logger.Warn("agent: save turn", "err", err)
	}
	return m.ID
}

// addNote stores a system note as a user message, so the model learns
// what happened outside the chat, such as an approval.
func (s *Service) addNote(ctx context.Context, sessionID, note string) {
	if sessionID == "" {
		return
	}
	if err := s.store.AddMessage(ctx, domain.Message{SessionID: sessionID,
		Role: "user", Content: "[System note: " + note + "]",
		CreatedAt: s.opt.Now()}); err != nil {
		s.opt.Logger.Warn("agent: save note", "err", err)
	}
}

// loadHistory rebuilds model turns from stored messages. The window
// starts at a plain user message, so it never begins with an orphan
// function call or result.
func (s *Service) loadHistory(ctx context.Context,
	sessionID string) ([]contract.Turn, error) {
	msgs, err := s.store.RecentMessages(ctx, sessionID, s.opt.HistoryMessages)
	if err != nil {
		return nil, err
	}
	start := len(msgs)
	for i, m := range msgs {
		if m.Role == "user" {
			start = i
			break
		}
	}
	var out []contract.Turn
	for _, m := range msgs[start:] {
		switch m.Role {
		case "user":
			out = appendTurn(out, contract.Turn{Role: "user", Text: m.Content})
		case "model":
			var sm storedModel
			if json.Unmarshal([]byte(m.Content), &sm) != nil {
				continue
			}
			t := contract.Turn{Role: "model", Text: sm.Text}
			for _, c := range sm.Calls {
				t.Calls = append(t.Calls, contract.FuncCall{Name: c.Name,
					Args: c.Args, Signature: c.Signature})
			}
			out = appendTurn(out, t)
		case "tool":
			var st storedTool
			if json.Unmarshal([]byte(m.Content), &st) != nil {
				continue
			}
			t := contract.Turn{Role: "user"}
			for _, r := range st.Results {
				t.Results = append(t.Results, contract.FuncResult{Name: r.Name,
					Output: r.Output})
			}
			out = appendTurn(out, t)
		}
	}
	return out, nil
}

// appendTurn adds t to the history. Two user turns in a row become one,
// because Gemini expects user and model turns to alternate.
func appendTurn(h []contract.Turn, t contract.Turn) []contract.Turn {
	if len(h) == 0 || t.Role != "user" || h[len(h)-1].Role != "user" {
		return append(h, t)
	}
	last := &h[len(h)-1]
	switch {
	case last.Text == "":
		last.Text = t.Text
	case t.Text != "":
		last.Text += "\n\n" + t.Text
	}
	last.Results = append(last.Results, t.Results...)
	return h
}
