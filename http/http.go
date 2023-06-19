package http

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

// region - common http client interface

type Client interface {
	Post(url string, data map[string]any) ([]byte, int, error)
	Get(url string, args map[string]any) ([]byte, int, error)
}

// endregion
// region - http client implementation

type httpClient struct {
	client *http.Client
	post   ServiceCall
	get    ServiceCall
}

func ClientBuilder(token string) *httpClient {
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
func (c *httpClient) WithBreaker(failureThreshold uint) *httpClient {
	c.post = withBreaker(c.post, failureThreshold)
	c.get = withBreaker(c.get, failureThreshold)
	return c
}
func (c *httpClient) WithRetry(retries uint, delay time.Duration) *httpClient {
	c.post = withRetry(c.post, retries, delay)
	c.get = withRetry(c.get, retries, delay)
	return c
}
func (c *httpClient) Build() Client {
	return c
}

func (c *httpClient) Post(url string, args map[string]any) ([]byte, int, error) {
	res, code, err := c.post(c.client, context.Background(), url, args)
	err = c.handleError(err)
	return res, code, err
}
func (c *httpClient) Get(url string, args map[string]any) ([]byte, int, error) {
	res, code, err := c.get(c.client, context.Background(), url, args)
	err = c.handleError(err)
	return res, code, err
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

func post(client *http.Client, ctx context.Context, url string, data map[string]any) (result []byte, code int, err error) {
	var b []byte
	if data != nil {
		b, err = json.Marshal(data)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
	} else {
		b = []byte("{}")
	}
	resp, err := client.Post(url, applicationJson, bytes.NewBuffer(b))
	rcode := responseCode(resp)
	if err != nil {
		return nil, rcode, NewHttpError(rcode, err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	b, e := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, rcode, NewHttpError(rcode, errors.New(string(b)))
	}
	return b, resp.StatusCode, e
}
func get(client *http.Client, ctx context.Context, queryUrl string, params map[string]any) (result []byte, code int, err error) {

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
		return nil, rcode, NewHttpError(rcode, err)
	}
	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)
	b, e := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, rcode, NewHttpError(rcode, errors.New(string(b)))
	}
	return b, resp.StatusCode, e
}

// endregion
// region - service call wrappers

type ServiceCall func(client *http.Client, ctx context.Context, url string, args map[string]any) ([]byte, int, error)

func withRetry(call ServiceCall, retries uint, delay time.Duration) ServiceCall {
	return func(client *http.Client, ctx context.Context, url string, args map[string]any) ([]byte, int, error) {
		var r uint
		for r = 0; ; r++ {
			response, code, err := call(client, ctx, url, args)
			if err == nil || r >= retries {
				return response, code, err
			}
			if code == 401 || code == 403 {
				return response, code, err
			}

			wait := delay
			//wait := delay << r
			//if wait > 32*time.Second {
			//	wait = 32 * time.Second
			//}
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
				return nil, http.StatusInternalServerError, ctx.Err()
			}
		}
	}
}
func withBreaker(call ServiceCall, failureThreshold uint) ServiceCall {
	var consecutiveFailures = 1
	var lastAttempt = time.Now()
	var m sync.RWMutex
	return func(client *http.Client, ctx context.Context, url string, args map[string]any) ([]byte, int, error) {
		m.RLock()
		d := consecutiveFailures - int(failureThreshold)
		if d >= 0 {
			wait := time.Second * 2 << d
			if wait > time.Minute*1 {
				wait = time.Minute * 1
			}
			shouldRetryAt := lastAttempt.Add(wait)
			if !time.Now().After(shouldRetryAt) {
				m.RUnlock()
				logger.Debug("[breaker] service unreachable; wait for %v", time.Duration(wait))
				return nil, http.StatusInternalServerError, BreakError{
					Wait: time.Duration(wait),
					// TODO: custom errors (?)
					Err: errors.New("service unreachable"),
				}
			}
		}
		m.RUnlock()
		response, code, err := call(client, ctx, url, args)
		m.Lock()
		defer m.Unlock()
		lastAttempt = time.Now()
		if err != nil {
			consecutiveFailures++
			return response, code, err
		}
		consecutiveFailures = 0
		return response, code, nil
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

// endregion
