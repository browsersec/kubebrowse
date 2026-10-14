// Package httpx provides the small HTTP-handler compatibility layer used while
// moving the application from Gin to Encore raw endpoints. It intentionally
// uses only the Go standard library so application handlers remain portable.
package httpx

import (
	"encoding/json"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
)

// H is a convenience type for JSON object responses.
type H map[string]interface{}

// HandlerFunc is a framework-neutral request handler.
type HandlerFunc func(*Context)

// ResponseWriter tracks the status code while implementing http.ResponseWriter.
type ResponseWriter struct {
	http.ResponseWriter
	status int
}

// WriteHeader records the response status before writing it.
func (w *ResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write records a successful status when a handler writes a body directly.
func (w *ResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

// Status returns the response status, defaulting to 200 when no status was set.
func (w *ResponseWriter) Status() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}

// Context holds a request, response writer, route parameters, and request data.
type Context struct {
	Request *http.Request
	Writer  *ResponseWriter
	params  map[string]string
	values  map[string]interface{}
}

// NewContext creates a Context for an Encore raw endpoint.
func NewContext(w http.ResponseWriter, r *http.Request, params map[string]string) *Context {
	return &Context{
		Request: r,
		Writer:  &ResponseWriter{ResponseWriter: w},
		params:  params,
		values:  make(map[string]interface{}),
	}
}

// JSON sends a JSON response.
func (c *Context) JSON(status int, value interface{}) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(status)
	_ = json.NewEncoder(c.Writer).Encode(value)
}

// ShouldBindJSON decodes a JSON request body.
func (c *Context) ShouldBindJSON(value interface{}) error {
	return json.NewDecoder(c.Request.Body).Decode(value)
}

// Param returns a named route parameter.
func (c *Context) Param(name string) string {
	return c.params[name]
}

// Query returns a query string parameter.
func (c *Context) Query(name string) string {
	return c.Request.URL.Query().Get(name)
}

// FormFile returns an uploaded multipart file.
func (c *Context) FormFile(name string) (*multipart.FileHeader, error) {
	file, header, err := c.Request.FormFile(name)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return header, nil
}

// Header sets a response header.
func (c *Context) Header(name, value string) {
	c.Writer.Header().Set(name, value)
}

// Cookie returns a request cookie value.
func (c *Context) Cookie(name string) (string, error) {
	cookie, err := c.Request.Cookie(name)
	if err != nil {
		return "", err
	}
	return cookie.Value, nil
}

// SetCookie writes a response cookie.
func (c *Context) SetCookie(name, value string, maxAge int, path, domain string, secure, httpOnly bool) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		MaxAge:   maxAge,
		Path:     path,
		Domain:   domain,
		Secure:   secure,
		HttpOnly: httpOnly,
		SameSite: http.SameSiteLaxMode,
	})
}

// Set stores a request-scoped value.
func (c *Context) Set(name string, value interface{}) {
	c.values[name] = value
}

// Get retrieves a request-scoped value.
func (c *Context) Get(name string) (interface{}, bool) {
	value, ok := c.values[name]
	return value, ok
}

// GetString retrieves a string request-scoped value.
func (c *Context) GetString(name string) string {
	value, ok := c.Get(name)
	if !ok {
		return ""
	}
	stringValue, _ := value.(string)
	return stringValue
}

// Redirect sends an HTTP redirect.
func (c *Context) Redirect(status int, location string) {
	http.Redirect(c.Writer, c.Request, location, status)
}

// Abort exists for compatibility with legacy middleware. Handlers return after
// calling it, so no additional work is needed in the raw-endpoint model.
func (c *Context) Abort() {}

// Next exists for compatibility with legacy middleware. Encore endpoints do
// not use a mutable middleware chain.
func (c *Context) Next() {}

// ClientIP returns the first forwarded address, falling back to RemoteAddr.
func (c *Context) ClientIP() string {
	if forwarded := c.Request.Header.Get("X-Forwarded-For"); forwarded != "" {
		return strings.TrimSpace(strings.Split(forwarded, ",")[0])
	}
	if host, _, err := net.SplitHostPort(c.Request.RemoteAddr); err == nil {
		return host
	}
	return c.Request.RemoteAddr
}
