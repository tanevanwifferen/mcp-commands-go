package mcpcommands

import "fmt"

// CommandError is an error the model should see as-is: a bad command line, or a
// refusal from a command. Any error a handler returns is shown by its message;
// CommandError exists so callers can tell these apart with errors.As.
type CommandError struct{ Msg string }

func (e *CommandError) Error() string { return e.Msg }

// Errorf returns a *CommandError with a formatted message.
func Errorf(format string, a ...any) error {
	return &CommandError{Msg: fmt.Sprintf(format, a...)}
}

// UsageError means the command line does not fit the command. The command's
// help is appended to the message. Return one from a handler (or from an
// input's Validate method) to reject a value with the usage attached.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Usagef returns a *UsageError with a formatted message.
func Usagef(format string, a ...any) error {
	return &UsageError{Msg: fmt.Sprintf(format, a...)}
}
