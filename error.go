package http

import "encoding/json"

type HttpError struct {
	code    int
	message string
}

func NewHttpError(code int, err error) error {
	var e map[string]string
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	er := json.Unmarshal([]byte(message), &e)
	if er == nil {
		v, ok := e["error"]
		if ok {
			return &HttpError{
				code:    code,
				message: v,
			}
		}
	}
	return &HttpError{
		code:    code,
		message: message,
	}
}
func (e *HttpError) Code() int {
	return e.code
}
func (e *HttpError) Error() string {
	return e.message
}
func (e *HttpError) Is(tgt error) bool {
	_, ok := tgt.(*HttpError)
	if !ok {
		return false
	}
	return true
}
