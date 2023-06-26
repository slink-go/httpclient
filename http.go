package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/slink-go/logger"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"
)

// -- TODO: CLIENT
// Throttle

// -- TODO: SERVER
// Tenants sharded map
// Throttle per client

const authHeader = "Authorization"
const applicationJson = "application/json"

const errorRegexpA = `(?P<METHOD>.*) (["])(?P<PROTO>[a-zA-Z]*:\/\/)(?P<HOST>[a-zA-Z0-9-_.:]+)\/(?P<PATH>.*)(["])(?P<MESSAGE>.*)`
const errorRegexpB = `(?P<METHOD>.*) (["])(?P<URL>.*)(["])(?P<MESSAGE>.*)`

var regexA *regexp.Regexp
var regexB *regexp.Regexp

func init() {
	regexA = regexp.MustCompile(errorRegexpA)
	regexB = regexp.MustCompile(errorRegexpB)
}

// region - common http client interface

type Client interface {
	Post(url string, data map[string]any, headers map[string]string) ([]byte, map[string]string, int, error)
	Get(url string, args map[string]any, headers map[string]string) ([]byte, map[string]string, int, error)
}

// endregion
// region - http client implementation

type httpClient struct {
	client *http.Client
	post   ServiceCall
	get    ServiceCall
}

func NewInsecureClient() *httpClient {
	return &httpClient{
		client: &http.Client{},
		post:   post,
		get:    get,
	}
}
func NewTokenAuthClient(token string) *httpClient {
	return &httpClient{
		client: &http.Client{
			Transport: authProxy{
				Transport: http.DefaultTransport,
				Token:     token,
			},
		},
		post: post,
		get:  get,
	}
}

func (c *httpClient) WithTimeout(timeout time.Duration) *httpClient {
	c.client.Timeout = timeout
	return c
}
func (c *httpClient) WithBreaker(failureThreshold uint, initial, max time.Duration) *httpClient {
	c.post = withBreaker(c.post, failureThreshold, initial, max)
	c.get = withBreaker(c.get, failureThreshold, initial, max)
	return c
}
func (c *httpClient) WithRetry(retries uint, delay time.Duration) *httpClient {
	c.post = withRetry(c.post, retries, delay)
	c.get = withRetry(c.get, retries, delay)
	return c
}

func (c *httpClient) Post(url string, args map[string]any, headers map[string]string) ([]byte, map[string]string, int, error) {
	res, hdrs, code, err := c.post(c.client, context.Background(), url, args, headers)
	err = c.handleError(err)
	return res, hdrs, code, err
}
func (c *httpClient) Get(url string, args map[string]any, headers map[string]string) ([]byte, map[string]string, int, error) {
	res, hdrs, code, err := c.get(c.client, context.Background(), url, args, headers)
	err = c.handleError(err)
	return res, hdrs, code, err
}

func (c *httpClient) handleError(err error) error {
	if err == nil {
		return nil
	}
	if os.IsTimeout(err) {
		// TODO: custom error
		return errors.New("client timeout")
	} else if errors.Is(err, BreakError{}) {
		// TODO: custom error
		return errors.New("service not available")
	} else if errors.Is(err, syscall.ECONNREFUSED) {
		// TODO: custom error
		return errors.New("connection refused")
	}
	return err
}

// endregion
// region - auth proxy

type authProxy struct {
	Transport http.RoundTripper
	Token     string
}

func (ap authProxy) RoundTrip(request *http.Request) (response *http.Response, e error) {
	if request.Header.Get(authHeader) == "" {
		request.Header.Set(authHeader, ap.bearerToken())
	}
	response, e = ap.Transport.RoundTrip(request)
	return
}
func (ap authProxy) bearerToken() string {
	return fmt.Sprintf("Bearer %s", ap.Token)
}

// endregion
// region - break error

type BreakError struct {
	Wait time.Duration
	Err  error
}

func (be BreakError) Error() string {
	return be.Err.Error()
}

// endregion
// region - http methods

func post(
	client *http.Client, ctx context.Context, url string,
	data map[string]any, headers map[string]string) (result []byte, hdr map[string]string, code int, err error) {

	var b []byte
	if data != nil {
		b, err = json.Marshal(data)
		if err != nil {
			code, err = processError(err, HttpClientMarshallingError)
			return nil, nil, code, err
		}
	} else {
		b = []byte("{}")
	}
	resp, err := client.Post(url, applicationJson, bytes.NewBuffer(b))
	rcode := responseCode(resp)
	if err != nil {
		code, err = processError(err, rcode)
		return nil, processResponseHeaders(resp), code, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	b, e := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		code, err = processError(errors.New(string(b)), rcode)
		return nil, processResponseHeaders(resp), code, err
	}
	code, err = processError(e, resp.StatusCode)
	return b, processResponseHeaders(resp), code, err
}
func get(
	client *http.Client, ctx context.Context, queryUrl string,
	params map[string]any, headers map[string]string) (result []byte, hdr map[string]string, code int, err error) {

	q := url.Values{}
	for k, v := range params {
		q.Add(k, fmt.Sprintf("%v", v))
	}

	encoded := q.Encode()

	if encoded != "" {
		if strings.Contains(queryUrl, "?") {
			queryUrl = queryUrl + "&" + encoded
		} else {
			queryUrl = queryUrl + "?" + encoded
		}
	}

	resp, err := client.Get(queryUrl)
	rcode := responseCode(resp)
	if err != nil {
		code, err = processError(err, rcode)
		return nil, nil, code, err
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	b, e := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		code, err = processError(errors.New(string(b)), rcode)
		return nil, processResponseHeaders(resp), code, err
	}
	code, err = processError(e, resp.StatusCode)
	return b, processResponseHeaders(resp), code, err
}

// endregion
// region - service call wrappers

type ServiceCall func(
	client *http.Client, ctx context.Context, url string,
	args map[string]any, headers map[string]string) ([]byte, map[string]string, int, error)

//func withThrottle(call ServiceCall) ServiceCall {
//	return func(client *http.Client, ctx context.Context, url string, args map[string]any) ([]byte, int, error) {
//		for {
//			response, code, err := call(client, ctx, url, args)
//			if code == 429 {
//				response.
//			}
//		}
//	}
//}

func withRetry(call ServiceCall, retries uint, delay time.Duration) ServiceCall {
	return func(
		client *http.Client, ctx context.Context, url string,
		args map[string]any, headers map[string]string) ([]byte, map[string]string, int, error) {
		var r uint
		for r = 0; ; r++ {
			response, hdrs, code, err := call(client, ctx, url, args, headers)
			if err == nil || /*code == 400 ||*/ code == http.StatusUnauthorized || code == http.StatusForbidden {
				return response, hdrs, code, err
			}
			if r >= retries {
				code, err = processError(err, HttpClientRetriesExhaustedError)
				return response, hdrs, code, err
			}
			wait := delay
			er, ok := err.(BreakError)
			if ok {
				wait = er.Wait
			}
			// add random jitter
			wait = wait + time.Duration(rand.Intn(250))*time.Millisecond
			logger.Debug("[retry] attempt %d failed; retying in %v", r+1, wait)
			select {
			case <-time.After(wait):
			case <-ctx.Done():
				return nil, nil, http.StatusInternalServerError, ctx.Err()
			}
		}
	}
}
func withBreaker(call ServiceCall, failureThreshold uint, initialDelay, maxDelay time.Duration) ServiceCall {
	if initialDelay > maxDelay {
		v := initialDelay
		maxDelay = initialDelay
		initialDelay = v
	}
	var consecutiveFailures = 1
	var lastAttempt = time.Now()
	var m sync.RWMutex
	return func(
		client *http.Client, ctx context.Context, url string,
		args map[string]any, headers map[string]string) ([]byte, map[string]string, int, error) {
		m.RLock()
		d := consecutiveFailures - int(failureThreshold)
		if d >= 0 {
			wait := time.Second * initialDelay << d
			if wait > maxDelay {
				wait = maxDelay
			}
			shouldRetryAt := lastAttempt.Add(wait)
			if !time.Now().After(shouldRetryAt) {
				m.RUnlock()
				logger.Debug("[breaker] service unreachable; wait for %v", time.Duration(wait))
				return nil, nil, http.StatusInternalServerError, BreakError{
					Wait: time.Duration(wait),
					// TODO: custom errors (?)
					Err: errors.New("service unreachable"),
				}
			}
		}
		m.RUnlock()
		response, hdrs, code, err := call(client, ctx, url, args, headers)
		m.Lock()
		defer m.Unlock()
		lastAttempt = time.Now()
		if err != nil {
			consecutiveFailures++
			return response, hdrs, code, err
		}
		consecutiveFailures = 0
		return response, hdrs, code, nil
	}
}

// endregion
// region - helpers

func responseCode(response *http.Response) int {
	if response == nil {
		return http.StatusInternalServerError
	}
	return response.StatusCode
}
func processResponseHeaders(response *http.Response) map[string]string {
	if response == nil {
		return map[string]string{}
	}
	result := map[string]string{}
	for k, h := range response.Header {
		for _, v := range h {
			result[k] = fmt.Sprintf("%s%s,", result[k], v)
		}
		result[k] = strings.TrimRight(result[k], ",")
	}
	return result
}

func parseError(input string) (method, proto, host, path, url, msg string, err error) {
	if input == "" {
		return
	}
	matchA := regexA.FindStringSubmatch(input)
	pmapA := make(map[string]string)
	for i, name := range regexA.SubexpNames() {
		if i > 0 && i < len(matchA) {
			pmapA[name] = matchA[i]
		}
	}
	matchB := regexB.FindStringSubmatch(input)
	pmapB := make(map[string]string)
	for i, name := range regexB.SubexpNames() {
		if i > 0 && i < len(matchB) {
			pmapB[name] = matchB[i]
		}
	}
	method = pmapA["METHOD"]
	proto = strings.Split(pmapA["PROTO"], ":")[0]
	host = pmapA["HOST"]
	host = strings.Split(host, ":")[0]
	path = pmapA["PATH"]
	msg = pmapA["MESSAGE"]
	url = pmapB["URL"]
	err = nil
	return
}

func processError(err error, code int) (int, error) {
	if err == nil {
		return 200, nil
	}
	if errors.Is(err, &ServiceUnreachableError{}) {
		er := err.(*ServiceUnreachableError)
		return er.Code(), er
	} else if errors.Is(err, &ConnectionRefusedError{}) {
		er := err.(*ConnectionRefusedError)
		return er.Code(), er
	} else if errors.Is(err, &HttpError{}) {
		er := err.(*HttpError)
		return er.Code(), er
	}
	_, _, h, _, url, ms, er := parseError(err.Error())
	if er != nil {
		return 500, er
	}
	parts := strings.Split(ms, ":")
	ms = strings.TrimSpace(parts[len(parts)-1])
	//logger.Warning("> parsed error: src=%s, mt=%s, pr=%s, h=%s, pa=%s, url=%s, ms=%s", err.Error(), mt, pr, h, pa, url, ms)
	code, err = processErrorMsg(url, h, ms, err.Error(), code)
	//logger.Warning("> processed error: %v", err)
	return code, err
}
func processErrorMsg(url, host, message, source string, code int) (int, error) {
	if message == "" || host == "" || url == "" {
		return code, errors.New(source)
	}
	if code == HttpClientRetriesExhaustedError {
		return code, NewServiceUnreachableError(
			HttpClientRetriesExhaustedError,
			fmt.Sprintf("%s: %s", message, url),
		)
	}
	if message == "no such host" {
		return HttpClientHoSuchHostError, NewHttpError(
			HttpClientHoSuchHostError,
			errors.New(fmt.Sprintf("%s: %s", message, host)),
		)
	}
	if message == "connection refused" || message == "service unavailable" {
		return HttpClientServiceUnavailableError, NewServiceUnreachableError(
			HttpClientConnectionRefusedError,
			fmt.Sprintf("%s: %s", message, url),
		)
	}
	//if code < HttpClientUnknownError {
	//	return NewHttpError(code, err)
	//}
	return code, errors.New(source)
}

// endregion
