package domain

import "fmt"

type Error struct {
	Code, Message string
	Details       map[string]any
}

func (e *Error) Error() string             { return e.Message }
func NewError(code, message string) *Error { return &Error{Code: code, Message: message} }
func Invalid(field, message string) *Error {
	return &Error{Code: "VALIDATION_FAILED", Message: message, Details: map[string]any{"field": field}}
}
func NotFound(kind, id string) *Error {
	return &Error{Code: "NOT_FOUND", Message: fmt.Sprintf("%s %s 不存在", kind, id)}
}
func Conflict(message string) *Error      { return NewError("CONFLICT", message) }
func StateConflict(message string) *Error { return NewError("INVALID_STATE", message) }
