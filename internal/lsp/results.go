package lsp

import (
	"bytes"
	"encoding/json"
)

// parseCompletion normaliza CompletionList | CompletionItem[] | null.
func parseCompletion(raw json.RawMessage) []CompletionItem {
	raw = bytes.TrimSpace(raw)
	var items []json.RawMessage
	switch {
	case len(raw) == 0:
		return nil
	case raw[0] == '[':
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
	case raw[0] == '{':
		var list struct {
			Items []json.RawMessage `json:"items"`
		}
		if json.Unmarshal(raw, &list) != nil {
			return nil
		}
		items = list.Items
	default:
		return nil
	}
	out := make([]CompletionItem, 0, len(items))
	for _, it := range items {
		var r struct {
			Label            string `json:"label"`
			Detail           string `json:"detail"`
			Kind             int    `json:"kind"`
			InsertText       string `json:"insertText"`
			InsertTextFormat int    `json:"insertTextFormat"`
			SortText         string `json:"sortText"`
			TextEdit         *struct {
				NewText string `json:"newText"`
				Range   *Range `json:"range"`
				Replace *Range `json:"replace"` // InsertReplaceEdit
			} `json:"textEdit"`
		}
		if json.Unmarshal(it, &r) != nil || r.Label == "" {
			continue
		}
		ci := CompletionItem{
			Label: r.Label, Detail: r.Detail, Kind: r.Kind,
			InsertText: r.InsertText, Snippet: r.InsertTextFormat == 2,
			SortText: r.SortText,
		}
		if r.TextEdit != nil {
			ci.InsertText = r.TextEdit.NewText
			ci.Replace = r.TextEdit.Range
			if r.TextEdit.Replace != nil {
				ci.Replace = r.TextEdit.Replace
			}
		}
		if ci.InsertText == "" {
			ci.InsertText = ci.Label
		}
		out = append(out, ci)
	}
	return out
}

// parseLocations normaliza Location | Location[] | LocationLink[] | null.
func parseLocations(raw json.RawMessage) []Location {
	raw = bytes.TrimSpace(raw)
	var items []json.RawMessage
	switch {
	case len(raw) == 0:
		return nil
	case raw[0] == '[':
		if json.Unmarshal(raw, &items) != nil {
			return nil
		}
	case raw[0] == '{':
		items = []json.RawMessage{raw}
	default:
		return nil
	}
	out := make([]Location, 0, len(items))
	for _, it := range items {
		var r struct {
			URI                  string `json:"uri"`
			Range                *Range `json:"range"`
			TargetURI            string `json:"targetUri"`
			TargetRange          *Range `json:"targetRange"`
			TargetSelectionRange *Range `json:"targetSelectionRange"`
		}
		if json.Unmarshal(it, &r) != nil {
			continue
		}
		switch {
		case r.URI != "" && r.Range != nil:
			out = append(out, Location{URI: r.URI, Range: *r.Range})
		case r.TargetURI != "":
			rg := r.TargetSelectionRange
			if rg == nil {
				rg = r.TargetRange
			}
			if rg != nil {
				out = append(out, Location{URI: r.TargetURI, Range: *rg})
			}
		}
	}
	return out
}
