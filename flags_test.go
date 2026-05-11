package main

import (
	"strings"
	"testing"
)

func TestParseChatFlags(t *testing.T) {
	cases := []struct {
		name       string
		in         []string
		wantProv   string
		wantModel  string
		wantYolo   bool
		wantPrompt string
		wantErr    string
	}{
		{
			name:       "no flags, prompt only",
			in:         []string{"chat", "hello", "world"},
			wantPrompt: "hello world",
		},
		{
			name:      "long flags before prompt",
			in:        []string{"chat", "--provider", "openai", "--model", "gpt-5", "say hi"},
			wantProv:  "openai",
			wantModel: "gpt-5",
			wantPrompt: "say hi",
		},
		{
			name:      "short flags",
			in:        []string{"chat", "-p", "openai", "-m", "gpt-5", "hi"},
			wantProv:  "openai",
			wantModel: "gpt-5",
			wantPrompt: "hi",
		},
		{
			name:       "equals form",
			in:         []string{"chat", "--provider=openai", "--model=gpt-5", "yo"},
			wantProv:   "openai",
			wantModel:  "gpt-5",
			wantPrompt: "yo",
		},
		{
			name:     "yolo as standalone",
			in:       []string{"chat", "--yolo"},
			wantYolo: true,
		},
		{
			name:     "yolo with explicit value",
			in:       []string{"chat", "--yolo=false"},
			wantYolo: false,
		},
		{
			name:       "double dash terminates flags",
			in:         []string{"chat", "--yolo", "--", "--this-is-prompt", "--also-prompt"},
			wantYolo:   true,
			wantPrompt: "--this-is-prompt --also-prompt",
		},
		{
			name:    "missing value for flag",
			in:      []string{"chat", "--provider"},
			wantErr: "expects a value",
		},
		{
			name:    "unknown flag rejected",
			in:      []string{"chat", "--bogus"},
			wantErr: "unknown flag",
		},
		{
			name:    "bad yolo value",
			in:      []string{"chat", "--yolo=maybe"},
			wantErr: "invalid value",
		},
		{
			name:       "non-flag stops parsing",
			in:         []string{"chat", "hi", "--this-is-text", "more"},
			wantPrompt: "hi --this-is-text more",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rest, flags, err := parseChatFlags(c.in)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("want err containing %q, got %v", c.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if flags.Provider != c.wantProv {
				t.Errorf("Provider = %q; want %q", flags.Provider, c.wantProv)
			}
			if flags.Model != c.wantModel {
				t.Errorf("Model = %q; want %q", flags.Model, c.wantModel)
			}
			if flags.Yolo != c.wantYolo {
				t.Errorf("Yolo = %v; want %v", flags.Yolo, c.wantYolo)
			}
			gotPrompt := ""
			if len(rest) > 1 {
				gotPrompt = strings.Join(rest[1:], " ")
			}
			if gotPrompt != c.wantPrompt {
				t.Errorf("prompt = %q; want %q", gotPrompt, c.wantPrompt)
			}
		})
	}
}
