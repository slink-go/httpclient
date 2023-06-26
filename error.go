package httpclient

import "encoding/json"

type httpClientError int

const (
	httpClientUnknownError httpClientError = iota + 0x100000
	httpClientMarshallingError
	httpClientServiceUnavailableError
	httpClientConnectionRefusedError
	httpClientRetriesExhaustedError
	httpClientHoSuchHostError
)

const (
	HttpClientUnknownError            = int(httpClientUnknownError)
	HttpClientMarshallingError        = int(httpClientMarshallingError)
	HttpClientServiceUnavailableError = int(httpClientServiceUnavailableError)
	HttpClientConnectionRefusedError  = int(httpClientConnectionRefusedError)
	HttpClientRetriesExhaustedError   = int(httpClientRetriesExhaustedError)
	HttpClientHoSuchHostError         = int(httpClientHoSuchHostError)
)

// region - HttpError

type HttpError struct {
	code    int
	message string
}

func NewHttpError(code int, err error) error {
	return &HttpError{
		code:    code,
		message: errorMessage(err),
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

// endregion
// region - ServiceUnreachableError

type ServiceUnreachableError struct {
	code    int
	message string
}

func NewServiceUnreachableError(code int, message string) error {
	return &ServiceUnreachableError{
		code:    code,
		message: message,
	}
}
func (e *ServiceUnreachableError) Code() int {
	return e.code
}
func (e *ServiceUnreachableError) Error() string {
	return e.message
}
func (e *ServiceUnreachableError) Is(tgt error) bool {
	_, ok := tgt.(*ServiceUnreachableError)
	if !ok {
		return false
	}
	return true
}

// endregion
// region - ConnectionRefusedError

type ConnectionRefusedError struct {
	code    int
	message string
}

func NewConnectionRefusedError(code int, err error) error {
	return &ConnectionRefusedError{
		code:    code,
		message: errorMessage(err),
	}
}
func (e *ConnectionRefusedError) Code() int {
	return e.code
}
func (e *ConnectionRefusedError) Error() string {
	return e.message
}
func (e *ConnectionRefusedError) Is(tgt error) bool {
	_, ok := tgt.(*ServiceUnreachableError)
	if !ok {
		return false
	}
	return true
}

func errorMessage(err error) string {
	var e map[string]string
	message := "unknown error"
	if err != nil {
		message = err.Error()
	}
	er := json.Unmarshal([]byte(message), &e)
	if er == nil {
		v, ok := e["error"]
		if ok {
			return v
		}
	}
	return message
}

// endregion
