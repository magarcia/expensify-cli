package fault

import "fmt"

// Error describes a failure without exposing credentials or transport internals.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
	Exit    int    `json:"-"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func New(code, message, hint string, exit int) *Error {
	return &Error{Code: code, Message: message, Hint: hint, Exit: exit}
}

func Usage(message string) *Error {
	return New("CLI-001", message, "Run the command with --help for usage and examples.", 2)
}
