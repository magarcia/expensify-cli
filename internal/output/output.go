package output

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/charmbracelet/x/term"
)

// Write keeps JSON lossless while making terminal and pipe output readable.
func Write(w io.Writer, value any, format string, tty, noColor bool) error {
	if format == "json" {
		enc := json.NewEncoder(w)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		return enc.Encode(value)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var data any
	dec := json.NewDecoder(bytes.NewReader(encoded))
	dec.UseNumber()
	if err := dec.Decode(&data); err != nil {
		return err
	}
	if format == "llm" {
		return writeLLM(w, data)
	}
	if rows, ok := data.([]any); ok {
		return writeTable(w, rows, tty, noColor)
	}
	if object, ok := data.(map[string]any); ok {
		if rows, ok := object["items"].([]any); ok {
			if err := writeTable(w, rows, tty, noColor); err != nil {
				return err
			}
			if tty && object["has_more"] == true {
				offsetValue, _ := object["offset"].(json.Number)
				limitValue, _ := object["limit"].(json.Number)
				offset, _ := offsetValue.Int64()
				limit, _ := limitValue.Int64()
				_, err := fmt.Fprintf(w, "More results: repeat with --offset %d.\n", offset+limit)
				return err
			}
			return nil
		}
		for _, key := range keys(object) {
			if _, err := fmt.Fprintf(w, "%s: %s\n", key, display(object[key])); err != nil {
				return err
			}
		}
		return nil
	}
	_, err = fmt.Fprintln(w, display(data))
	return err
}

func writeTable(w io.Writer, rows []any, tty, noColor bool) error {
	if len(rows) == 0 {
		if tty {
			_, err := fmt.Fprintln(w, "No results.")
			return err
		}
		return nil
	}
	columns := map[string]any{}
	for _, row := range rows {
		object, ok := row.(map[string]any)
		if !ok {
			return writeLLM(w, rows)
		}
		for key := range object {
			columns[key] = nil
		}
	}
	fields := keys(columns)
	if tty {
		fields = terminalColumns(columns)
	}
	width := 120
	if file, ok := w.(*os.File); tty && ok {
		if size, _, err := term.GetSize(file.Fd()); err == nil && size > 0 {
			width = size
		}
	}
	maxCell := max(8, (width-2*len(fields))/max(1, len(fields)))
	var target io.Writer = w
	var table *tabwriter.Writer
	if tty {
		table = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		target = table
		header := strings.ToUpper(strings.Join(fields, "\t"))
		if !noColor {
			header = "\x1b[36m" + header + "\x1b[0m"
		}
		if _, err := fmt.Fprintln(target, header); err != nil {
			return err
		}
	}
	for _, row := range rows {
		object := row.(map[string]any)
		cells := make([]string, len(fields))
		for i, field := range fields {
			cells[i] = display(object[field])
			if tty {
				cells[i] = truncate(cells[i], maxCell)
			}
		}
		if _, err := fmt.Fprintln(target, strings.Join(cells, "\t")); err != nil {
			return err
		}
	}
	if table != nil {
		return table.Flush()
	}
	return nil
}

func writeLLM(w io.Writer, value any) error {
	switch v := value.(type) {
	case []any:
		if len(v) == 0 {
			_, err := fmt.Fprintln(w, "No results.")
			return err
		}
		for i, item := range v {
			if _, err := fmt.Fprintf(w, "Record %d\n", i+1); err != nil {
				return err
			}
			if err := writeLLM(w, item); err != nil {
				return err
			}
			if i < len(v)-1 {
				if _, err := fmt.Fprintln(w); err != nil {
					return err
				}
			}
		}
	case map[string]any:
		for _, key := range keys(v) {
			if key == "items" {
				continue
			}
			if _, err := fmt.Fprintf(w, "- %s: %s\n", key, display(v[key])); err != nil {
				return err
			}
		}
		if items, ok := v["items"].([]any); ok {
			return writeLLM(w, items)
		}
	default:
		_, err := fmt.Fprintln(w, display(value))
		return err
	}
	return nil
}

func keys(object map[string]any) []string {
	result := make([]string, 0, len(object))
	for key := range object {
		result = append(result, key)
	}
	priority := map[string]int{"id": 1, "transactionID": 1, "reportID": 2, "report_id": 2, "policyID": 3, "policy_id": 3, "name": 4, "title": 4, "merchant": 4, "date": 5, "amount": 6, "total": 6, "currency": 7, "status": 8}
	sort.Slice(result, func(i, j int) bool {
		a, b := priority[result[i]], priority[result[j]]
		if a == 0 {
			a = 100
		}
		if b == 0 {
			b = 100
		}
		if a == b {
			return result[i] < result[j]
		}
		return a < b
	})
	return result
}

func terminalColumns(columns map[string]any) []string {
	preferred := []string{"id", "name", "type", "currency", "role"}
	if _, ok := columns["merchant"]; ok {
		preferred = []string{"id", "date", "merchant", "amount", "currency", "category"}
	} else if _, ok := columns["total"]; ok {
		preferred = []string{"id", "name", "total", "currency", "status"}
	} else if _, ok := columns["enabled"]; ok {
		preferred = []string{"name", "enabled", "group"}
	}
	fields := []string{}
	for _, key := range preferred {
		if _, ok := columns[key]; ok {
			fields = append(fields, key)
		}
	}
	if len(fields) == 0 {
		return keys(columns)
	}
	return fields
}

func display(value any) string {
	var text string
	switch v := value.(type) {
	case nil:
		return ""
	case string:
		text = v
	case json.Number:
		text = v.String()
	default:
		encoded, _ := json.Marshal(v)
		text = string(encoded)
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return ' '
		}
		return r
	}, text)
}

func truncate(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:max(0, limit-3)]) + "..."
}
