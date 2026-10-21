// Package claude runs Claude Code sessions in headless stream-json mode and
// relays their permission prompts and questions back to the TUI.
package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

// streamMsg is one line of `claude -p --output-format stream-json`.
type streamMsg struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype"`
	SessionID string          `json:"session_id"`
	Message   *messageJSON    `json:"message"`
	RequestID string          `json:"request_id"`
	Request   *controlRequest `json:"request"`

	// Fields of the final "result" message.
	Result       string  `json:"result"`
	IsError      bool    `json:"is_error"`
	TotalCostUSD float64 `json:"total_cost_usd"`
}

type messageJSON struct {
	Content json.RawMessage `json:"content"`
}

type contentBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input"`
	Content json.RawMessage `json:"content"`
	IsError bool            `json:"is_error"`
}

type controlRequest struct {
	Subtype     string          `json:"subtype"`
	ToolName    string          `json:"tool_name"`
	DisplayName string          `json:"display_name"`
	Input       json.RawMessage `json:"input"`
	Description string          `json:"description"`
	Suggestions json.RawMessage `json:"permission_suggestions"`
	ToolUseID   string          `json:"tool_use_id"`
}

// LineKind classifies a transcript line for rendering.
type LineKind int

const (
	LineText LineKind = iota
	LineTool
	LineToolError
	LineUser
	LineInfo
	LineError
	LineDone
)

// Line is one rendered entry of a session transcript.
type Line struct {
	Kind LineKind
	Text string
}

// Question mirrors one entry of the AskUserQuestion tool input.
type Question struct {
	Question    string   `json:"question"`
	Header      string   `json:"header"`
	Options     []Option `json:"options"`
	MultiSelect bool     `json:"multiSelect"`
}

// Option is a single answer choice of a Question.
type Option struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// Request is a pending prompt from Claude that needs the user.
type Request struct {
	SessionID   int
	RequestID   string
	ToolName    string
	Description string
	Summary     string
	Input       json.RawMessage
	Suggestions json.RawMessage
	// Questions is set when the request is an AskUserQuestion call.
	Questions []Question
}

// IsQuestion reports whether the request asks the user questions rather
// than asking permission to run a tool.
func (r Request) IsQuestion() bool { return r.ToolName == "AskUserQuestion" && len(r.Questions) > 0 }

// Answer is the user's response to a Request.
type Answer struct {
	Allow bool
	// Always also applies Claude's suggested permission rules for the rest of
	// the session.
	Always bool
	// Message explains a denial to Claude.
	Message string
	// Answers maps question text to the chosen label(s), for AskUserQuestion.
	Answers map[string]string
}

func newRequest(sessionID int, m streamMsg) Request {
	r := Request{
		SessionID:   sessionID,
		RequestID:   m.RequestID,
		ToolName:    m.Request.ToolName,
		Description: m.Request.Description,
		Input:       m.Request.Input,
		Suggestions: m.Request.Suggestions,
	}
	if m.Request.DisplayName != "" && m.Request.DisplayName != r.ToolName {
		r.ToolName = m.Request.DisplayName
	}
	r.Summary = summarizeTool(m.Request.ToolName, m.Request.Input, 2000)
	if m.Request.ToolName == "AskUserQuestion" {
		var in struct {
			Questions []Question `json:"questions"`
		}
		if json.Unmarshal(m.Request.Input, &in) == nil {
			r.Questions = in.Questions
		}
	}
	return r
}

// controlResponse builds the stdin line that answers req.
func controlResponse(req Request, a Answer) ([]byte, error) {
	var resp map[string]any
	switch {
	case !a.Allow:
		msg := a.Message
		if msg == "" {
			msg = "The user denied this action."
		}
		resp = map[string]any{"behavior": "deny", "message": msg}
	default:
		input := map[string]any{}
		if len(req.Input) > 0 {
			if err := json.Unmarshal(req.Input, &input); err != nil {
				return nil, fmt.Errorf("tool input: %w", err)
			}
		}
		if a.Answers != nil {
			input["answers"] = a.Answers
		}
		resp = map[string]any{"behavior": "allow", "updatedInput": input}
		if a.Always && len(req.Suggestions) > 0 && string(req.Suggestions) != "null" {
			resp["updatedPermissions"] = json.RawMessage(req.Suggestions)
		}
	}
	return json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": req.RequestID,
			"response":   resp,
		},
	})
}

// controlError answers a control request Fast Lane does not support.
func controlError(requestID, msg string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      msg,
		},
	})
	return b
}

// userMessage builds the stdin line that sends text to Claude.
func userMessage(text string) []byte {
	b, _ := json.Marshal(map[string]any{
		"type":    "user",
		"message": map[string]any{"role": "user", "content": text},
	})
	return b
}

// describe turns an assistant or user stream message into transcript lines.
func describe(m streamMsg) []Line {
	if m.Message == nil {
		return nil
	}
	var blocks []contentBlock
	if err := json.Unmarshal(m.Message.Content, &blocks); err != nil {
		var s string
		if json.Unmarshal(m.Message.Content, &s) == nil && s != "" && m.Type == "assistant" {
			return []Line{{LineText, s}}
		}
		return nil
	}
	var out []Line
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" && m.Type == "assistant" {
				out = append(out, Line{LineText, b.Text})
			}
		case "tool_use":
			out = append(out, Line{LineTool, b.Name + ": " + summarizeTool(b.Name, b.Input, 160)})
		case "tool_result":
			if b.IsError {
				out = append(out, Line{LineToolError, oneLine(resultText(b.Content), 200)})
			}
		}
	}
	return out
}

// summarizeTool picks the most telling field of a tool's input.
func summarizeTool(name string, input json.RawMessage, max int) string {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "pattern", "url", "query", "path", "description", "prompt"} {
		if v, ok := in[key].(string); ok && v != "" {
			if max > 200 {
				return truncate(v, max)
			}
			return oneLine(v, max)
		}
	}
	if name == "AskUserQuestion" {
		return "asking a question"
	}
	b, _ := json.Marshal(in)
	return oneLine(string(b), max)
}

func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) == nil {
		var parts []string
		for _, b := range blocks {
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		}
		return strings.Join(parts, " ")
	}
	return string(raw)
}

func oneLine(s string, max int) string {
	return truncate(strings.Join(strings.Fields(s), " "), max)
}

func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
