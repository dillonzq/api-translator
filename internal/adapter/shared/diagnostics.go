package shared

import (
	"encoding/json"
	"fmt"
	"github.com/dillonzq/api-translator/internal/errclass"
	"sort"
	"strings"
)

const (
	FormatChatCompletions = "chat-completions"
	FormatResponses       = "responses"
	FormatMessages        = "messages"
)

// LossPolicy controls request conversions that cannot preserve every field.
type LossPolicy string

const (
	LossPolicyAllow  LossPolicy = "allow"
	LossPolicySafe   LossPolicy = "safe"
	LossPolicyStrict LossPolicy = "strict"
)

// Diagnostic describes a concrete field omitted or approximated by conversion.
// Messages contain field names, never request values or upstream content.
type Diagnostic struct {
	Code     string
	Path     string
	Message  string
	Semantic bool
	From     string
	To       string
}

// ConversionOptions belongs to one RequestState. Safe rejects semantic request
// loss; Strict rejects all reported request loss. Responses report losses but
// are never retried or rejected solely because of this option.
type ConversionOptions struct{ LossPolicy LossPolicy }

func (s *ResponseTools) Diagnostics() []Diagnostic {
	if s == nil {
		return nil
	}
	return append([]Diagnostic(nil), s.diagnostics...)
}
func (s *ResponseTools) Record(d Diagnostic) {
	if s == nil {
		return
	}
	for _, prev := range s.diagnostics {
		if prev == d {
			return
		}
	}
	s.diagnostics = append(s.diagnostics, d)
}
func (s *ResponseTools) Report(from, to, path string, semantic bool) {
	s.Record(DiagnosticFor(from, to, path, semantic))
}
func (s *ResponseTools) RejectLoss(d Diagnostic) *errclass.Error {
	s.Record(d)
	if s != nil && (s.Options.LossPolicy == LossPolicyStrict || (s.Options.LossPolicy == LossPolicySafe && d.Semantic)) {
		return &errclass.Error{Class: errclass.ClassUnsupported, Message: d.Message}
	}
	return nil
}
func DiagnosticFor(from, to, path string, semantic bool) Diagnostic {
	path = errclass.Redact(path)
	return Diagnostic{Code: "unrepresentable_field", Path: path, Message: "conversion cannot preserve " + path, Semantic: semantic, From: from, To: to}
}
func requestDiagnostics(from, to string, body []byte) []Diagnostic {
	if from == to {
		return nil
	}
	var doc map[string]json.RawMessage
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	var out []Diagnostic
	add := func(path string, semantic bool) { out = append(out, DiagnosticFor(from, to, path, semantic)) }
	// Top-level fields supported by the direct converters. Mapped controls are
	// handled separately; unknown extension fields are reported rather than hidden.
	supported := map[string]bool{"model": true, "stream": true, "temperature": true, "top_p": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true}
	switch from {
	case FormatChatCompletions:
		for _, k := range []string{"messages", "max_tokens", "max_completion_tokens", "reasoning_effort", "response_format"} {
			supported[k] = true
		}
		supported["stop"] = to == FormatMessages
	case FormatResponses:
		for _, k := range []string{"input", "instructions", "max_output_tokens", "reasoning", "text"} {
			supported[k] = true
		}
	case FormatMessages:
		for _, k := range []string{"messages", "system", "max_tokens", "thinking", "output_config"} {
			supported[k] = true
		}
		supported["stop_sequences"] = to == FormatChatCompletions
	}
	for key, raw := range doc {
		if !supported[key] && string(raw) != "null" {
			add(key, key != "metadata" && key != "stream_options" && key != "user")
		}
	}
	var tools []map[string]json.RawMessage
	json.Unmarshal(doc["tools"], &tools)
	var inspectTool func(map[string]json.RawMessage, string)
	inspectTool = func(tool map[string]json.RawMessage, path string) {
		var typ, name string
		json.Unmarshal(tool["type"], &typ)
		json.Unmarshal(tool["name"], &name)
		if from == FormatChatCompletions {
			if typ != "function" {
				add(path, true)
			}
			var fn map[string]json.RawMessage
			json.Unmarshal(tool["function"], &fn)
			tool = fn
		} else if from == FormatResponses {
			if typ == "namespace" {
				var children []map[string]json.RawMessage
				json.Unmarshal(tool["tools"], &children)
				for i, child := range children {
					var childType string
					json.Unmarshal(child["type"], &childType)
					if IsSearchTool(childType) {
						add(fmt.Sprintf("%s.tools[%d]", path, i), true)
					}
					inspectTool(child, fmt.Sprintf("%s.tools[%d]", path, i))
				}
				return
			}
			if typ != "function" && typ != "custom" && !(to == FormatMessages && (typ == "web_search" || typ == "web_search_preview")) {
				add(path, true)
			}
			if typ == "custom" && len(tool["format"]) > 0 {
				add(path+".format", true)
			}
			if (typ == "web_search" || typ == "web_search_preview") && len(tool["search_context_size"]) > 0 {
				add(path+".search_context_size", true)
			}
		} else if typ != "" && typ != "custom" && !strings.HasPrefix(typ, "web_search_") {
			add(path, true)
		} else if strings.HasPrefix(typ, "web_search_") && to != FormatResponses {
			add(path, true)
		}
		if from == FormatMessages && strings.HasPrefix(typ, "web_search_") && to == FormatResponses {
			for _, key := range []string{"max_uses", "blocked_domains"} {
				if len(tool[key]) > 0 {
					add(path+"."+key, true)
				}
			}
		}
		known := map[string]bool{"type": true, "name": true, "description": true, "parameters": true, "input_schema": true, "strict": true, "cache_control": true, "format": true}
		if (from == FormatResponses && (typ == "web_search" || typ == "web_search_preview")) || (from == FormatMessages && strings.HasPrefix(typ, "web_search_")) {
			for _, key := range []string{"user_location", "filters", "allowed_domains", "blocked_domains", "max_uses", "search_context_size"} {
				known[key] = true
			}
		}
		for key := range tool {
			if !known[key] {
				add(path+"."+key, true)
			}
		}
		if len(tool["cache_control"]) > 0 {
			add(path+".cache_control", false)
		}
	}
	for i, tool := range tools {
		inspectTool(tool, fmt.Sprintf("tools[%d]", i))
	}
	// Inspect only protocol envelopes and content blocks. Tool input, JSON
	// schemas, and tool result payloads are business data, never traversed.
	inspectBlock := func(block map[string]json.RawMessage, path string) {
		var typ string
		json.Unmarshal(block["type"], &typ)
		if from == FormatMessages && typ == "document" {
			for _, key := range []string{"context", "citations"} {
				if HasContent(block[key]) {
					add(path+"."+key, true)
				}
			}
		}
		if from == FormatMessages && (typ == "thinking" || typ == "redacted_thinking") {
			add(path, true)
		}
		for _, key := range []string{"cache_control", "signature"} {
			if len(block[key]) > 0 {
				add(path+"."+key, false)
			}
		}
	}
	if from == FormatMessages || from == FormatChatCompletions {
		var messages []map[string]json.RawMessage
		json.Unmarshal(doc["messages"], &messages)
		for i, message := range messages {
			path := fmt.Sprintf("messages[%d]", i)
			if from == FormatChatCompletions {
				for _, key := range []string{"reasoning_content", "reasoning_details", "reasoning"} {
					if len(message[key]) > 0 {
						add(path+"."+key, true)
					}
				}
			}
			var blocks []map[string]json.RawMessage
			json.Unmarshal(message["content"], &blocks)
			for j, block := range blocks {
				inspectBlock(block, fmt.Sprintf("%s.content[%d]", path, j))
			}
		}
		var system []map[string]json.RawMessage
		json.Unmarshal(doc["system"], &system)
		for i, block := range system {
			inspectBlock(block, fmt.Sprintf("system[%d]", i))
		}
	} else if from == FormatResponses {
		var items []map[string]json.RawMessage
		json.Unmarshal(doc["input"], &items)
		for i, item := range items {
			path := fmt.Sprintf("input[%d]", i)
			var typ string
			json.Unmarshal(item["type"], &typ)
			if typ == "reasoning" {
				if to == FormatChatCompletions {
					add(path, true)
				}
				if len(item["encrypted_content"]) > 0 {
					add(path+".encrypted_content", true)
				}
			}
			if typ == "message" {
				var parts []map[string]json.RawMessage
				json.Unmarshal(item["content"], &parts)
				for j, part := range parts {
					if len(part["cache_control"]) > 0 {
						add(fmt.Sprintf("%s.content[%d].cache_control", path, j), false)
					}
				}
			}
			if typ == "additional_tools" {
				var extra []map[string]json.RawMessage
				json.Unmarshal(item["tools"], &extra)
				for j, tool := range extra {
					inspectTool(tool, fmt.Sprintf("%s.tools[%d]", path, j))
				}
			}
		}
	}
	// Report protocol-specific reasoning and output configuration siblings.
	for _, pair := range []struct {
		parent string
		kept   []string
	}{{"reasoning", []string{"effort"}}, {"output_config", []string{"format", "effort"}}, {"text", []string{"format"}}} {
		var obj map[string]json.RawMessage
		json.Unmarshal(doc[pair.parent], &obj)
		for k := range obj {
			keep := false
			for _, v := range pair.kept {
				if k == v {
					keep = true
				}
			}
			if !keep {
				add(pair.parent+"."+k, true)
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
func (state *ResponseTools) CheckRequestLosses(from, to string, body []byte) *errclass.Error {
	if state != nil {
		switch state.Options.LossPolicy {
		case "", LossPolicyAllow, LossPolicySafe, LossPolicyStrict:
		default:
			return errclass.Translation("invalid conversion loss policy")
		}
	}
	diagnostics := requestDiagnostics(from, to, body)
	for _, d := range diagnostics {
		state.Record(d)
	}
	for _, d := range diagnostics {
		if state != nil && (state.Options.LossPolicy == LossPolicyStrict || (state.Options.LossPolicy == LossPolicySafe && d.Semantic)) {
			return &errclass.Error{Class: errclass.ClassUnsupported, Message: d.Message}
		}
	}
	return nil
}

func (state *ResponseTools) RecordResponseLosses(from, to string, body []byte) {
	var doc map[string]json.RawMessage
	json.Unmarshal(body, &doc)
	for _, key := range []string{"metadata", "service_tier", "system_fingerprint", "incomplete_details"} {
		if len(doc[key]) > 0 {
			state.Record(DiagnosticFor(from, to, key, false))
		}
	}
	if from == FormatChatCompletions {
		var choices []map[string]json.RawMessage
		json.Unmarshal(doc["choices"], &choices)
		if len(choices) > 1 {
			state.Record(DiagnosticFor(from, to, "choices[1:]", true))
		}
		for i, choice := range choices {
			if len(choice["logprobs"]) > 0 && string(choice["logprobs"]) != "null" {
				state.Record(DiagnosticFor(from, to, fmt.Sprintf("choices[%d].logprobs", i), false))
			}
		}
	}
}
