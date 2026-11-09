package store

import "errors"

var ErrStateConflict = errors.New("resource state conflicts with request")
var ErrInvalidInput = errors.New("invalid request input")

type classifiedError struct {
	cause error
	class error
}

func (e classifiedError) Error() string        { return e.cause.Error() }
func (e classifiedError) Unwrap() error        { return e.cause }
func (e classifiedError) Is(target error) bool { return target == e.class }
func stateConflict(message string) error {
	return classifiedError{errors.New(message), ErrStateConflict}
}
func invalidInput(err error) error { return classifiedError{err, ErrInvalidInput} }
