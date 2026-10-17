package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestFormats(t *testing.T) {
	rows := []map[string]any{{"transactionID": "9007199254740993", "merchant": "Cafe\nEspresso", "amount": 1234, "currency": "EUR"}}
	for _, format := range []string{"text", "json", "llm"} {
		t.Run(format, func(t *testing.T) {
			var b bytes.Buffer
			if err := Write(&b, rows, format, false, true); err != nil {
				t.Fatal(err)
			}
			switch format {
			case "json":
				if !json.Valid(b.Bytes()) || !strings.Contains(b.String(), "9007199254740993") {
					t.Fatal(b.String())
				}
			case "text":
				if b.String() != "9007199254740993\tCafe Espresso\t1234\tEUR\n" {
					t.Fatalf("unexpected TSV: %q", b.String())
				}
			case "llm":
				if !strings.Contains(b.String(), "- transactionID: 9007199254740993") || strings.Contains(b.String(), "\x1b") {
					t.Fatal(b.String())
				}
			}
		})
	}
}

func TestTerminalAndEmpty(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, []map[string]any{{"merchant": strings.Repeat("x", 200)}}, "text", true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "MERCHANT") || !strings.Contains(b.String(), "...") || strings.Contains(b.String(), "\x1b") {
		t.Fatal(b.String())
	}
	b.Reset()
	if err := Write(&b, []any{}, "text", false, true); err != nil {
		t.Fatal(err)
	}
	if b.Len() != 0 {
		t.Fatal(b.String())
	}
}

func TestTextSanitizesControlCharacters(t *testing.T) {
	var b bytes.Buffer
	if err := Write(&b, map[string]string{"merchant": "a\x1b[31m\tb\r\nc"}, "text", false, true); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(b.String(), "\x1b\t\r") {
		t.Fatalf("terminal control leaked: %q", b.String())
	}
}

func TestPaginatedOutput(t *testing.T) {
	page := map[string]any{"items": []map[string]any{{"id": "123", "merchant": "Cafe", "amount": 1234}}, "offset": 0, "limit": 1, "has_more": true}
	for _, format := range []string{"text", "llm", "json"} {
		var b bytes.Buffer
		if err := Write(&b, page, format, false, true); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(b.String(), "123") {
			t.Fatal(b.String())
		}
		if format == "text" && b.String() != "123\tCafe\t1234\n" {
			t.Fatalf("TSV: %q", b.String())
		}
		if format == "llm" && !strings.Contains(b.String(), "- has_more: true") {
			t.Fatal(b.String())
		}
	}
	var b bytes.Buffer
	if err := Write(&b, page, "text", true, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(b.String(), "--offset 1") {
		t.Fatal(b.String())
	}
}
