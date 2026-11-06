package llm

import (
	"encoding/base64"
	"strings"

	"paidup-agent/internal/contract"
)

// Wire types for generateContent. Only the fields PaidUp uses.

type generateRequest struct {
	SystemInstruction *content          `json:"systemInstruction,omitempty"`
	Contents          []content         `json:"contents"`
	Tools             []tool            `json:"tools,omitempty"`
	GenerationConfig  *generationConfig `json:"generationConfig,omitempty"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type part struct {
	Text             string            `json:"text,omitempty"`
	Thought          bool              `json:"thought,omitempty"`
	InlineData       *inlineData       `json:"inlineData,omitempty"`
	FunctionCall     *functionCall     `json:"functionCall,omitempty"`
	FunctionResponse *functionResponse `json:"functionResponse,omitempty"`
	// ThoughtSignature stays a string: it is opaque, and the client sends
	// it back exactly as received.
	ThoughtSignature string `json:"thoughtSignature,omitempty"`
}

type inlineData struct {
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"` // base64
}

type functionCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args,omitempty"`
}

type functionResponse struct {
	Name     string         `json:"name"`
	Response map[string]any `json:"response"`
}

type tool struct {
	FunctionDeclarations []functionDecl `json:"functionDeclarations"`
}

type functionDecl struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type generationConfig struct {
	ResponseMIMEType string         `json:"responseMimeType,omitempty"`
	ResponseSchema   map[string]any `json:"responseSchema,omitempty"`
}

type generateResponse struct {
	Candidates []struct {
		Content      content `json:"content"`
		FinishReason string  `json:"finishReason"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback,omitempty"`
}

// toContents maps chat history to Gemini contents. A user turn carries
// function results, text, and an image, in that order. A model turn
// carries text and function calls, each with its thought signature.
func toContents(history []contract.Turn) []content {
	out := make([]content, 0, len(history))
	for _, t := range history {
		role := t.Role
		if role != "model" {
			role = "user"
		}
		var parts []part
		// Function results come first, so they follow the model's
		// calls.
		for _, fr := range t.Results {
			resp := fr.Output
			if resp == nil {
				resp = map[string]any{}
			}
			parts = append(parts, part{FunctionResponse: &functionResponse{
				Name: fr.Name, Response: resp,
			}})
		}
		if t.Text != "" {
			parts = append(parts, part{Text: t.Text})
		}
		if len(t.Image) > 0 {
			parts = append(parts, part{InlineData: &inlineData{
				MIMEType: t.MIMEType,
				Data:     base64.StdEncoding.EncodeToString(t.Image),
			}})
		}
		for _, fc := range t.Calls {
			parts = append(parts, part{
				FunctionCall:     &functionCall{Name: fc.Name, Args: fc.Args},
				ThoughtSignature: string(fc.Signature),
			})
		}
		if len(parts) == 0 {
			continue
		}
		out = append(out, content{Role: role, Parts: parts})
	}
	return out
}

// fromResponse reads the first candidate. Thought parts are skipped.
func fromResponse(r generateResponse) contract.ChatResponse {
	var out contract.ChatResponse
	if len(r.Candidates) == 0 {
		return out
	}
	var text []string
	for _, p := range r.Candidates[0].Content.Parts {
		switch {
		case p.FunctionCall != nil:
			call := contract.FuncCall{Name: p.FunctionCall.Name,
				Args: p.FunctionCall.Args}
			if p.ThoughtSignature != "" {
				call.Signature = []byte(p.ThoughtSignature)
			}
			out.Calls = append(out.Calls, call)
		case p.Thought:
			// Internal reasoning; not for the user.
		case p.Text != "":
			text = append(text, p.Text)
		}
	}
	out.Text = strings.Join(text, "")
	return out
}

// normalizeSchema copies a JSON schema and upper-cases every "type"
// value. Gemini documents its schema types in upper case (OBJECT,
// STRING, ...), while tool authors usually write JSON Schema in lower
// case.
func normalizeSchema(s map[string]any) map[string]any {
	if s == nil {
		return nil
	}
	out := make(map[string]any, len(s))
	for k, v := range s {
		switch val := v.(type) {
		case string:
			if k == "type" {
				val = strings.ToUpper(val)
			}
			out[k] = val
		case map[string]any:
			if k == "properties" {
				props := make(map[string]any, len(val))
				for name, p := range val {
					if pm, ok := p.(map[string]any); ok {
						props[name] = normalizeSchema(pm)
					} else {
						props[name] = p
					}
				}
				out[k] = props
			} else {
				out[k] = normalizeSchema(val)
			}
		default:
			out[k] = v
		}
	}
	return out
}
