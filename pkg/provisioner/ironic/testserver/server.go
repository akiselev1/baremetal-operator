package testserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// New returns a MockServer
func New(t *testing.T, name string) *MockServer {
	mux := http.NewServeMux()
	t.Logf("%s: new server created", name)
	return &MockServer{
		t:                 t,
		name:              name,
		mux:               mux,
		responsesByMethod: make(map[string]map[string]response),
		defaultResponses:  []defaultResponse{},
	}
}

type response struct {
	code    int
	payload string
}

type defaultResponse struct {
	response

	method string
	re     *regexp.Regexp
}

// MockServer is a simple http testing server
type MockServer struct {
	t            *testing.T
	mux          *http.ServeMux
	name         string
	Requests     string
	FullRequests []*http.Request
	server       *httptest.Server
	errorCode    int

	responsesByMethod map[string]map[string]response
	defaultResponses  []defaultResponse
}

// Endpoint returns the URL to the server
func (m *MockServer) Endpoint() string {
	if m == nil || m.server == nil {
		// The consumer of this method expects something valid, but
		// won't use it if m is nil.
		return "https://ironic.test/v1/"
	}
	response := m.server.URL + "/v1/"
	m.t.Logf("%s: endpoint: %s", m.name, response)
	return response
}

func (m *MockServer) logRequest(r *http.Request, response string) {
	m.t.Logf("%s: %s %s -> %s", m.name, r.Method, r.URL, response)
	m.Requests += r.RequestURI + ";"
	m.FullRequests = append(m.FullRequests, r)
}

// Handler attaches a generic handler function to a request URL pattern
func (m *MockServer) Handler(pattern string, handlerFunc http.HandlerFunc) *MockServer {
	m.t.Logf("%s: adding handler for %s", m.name, pattern)
	m.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		m.logRequest(r, "(custom)")
		handlerFunc(w, r)
	})
	return m
}

// NotFound attaches a 404 error handler to a request URL pattern
func (m *MockServer) NotFound(pattern string) *MockServer {
	m.ErrorResponse(pattern, http.StatusNotFound)
	return m
}

// Response attaches a handler function that returns the given payload
// from requests to the URL pattern
func (m *MockServer) Response(pattern string, payload string) *MockServer {
	return m.ResponseWithCode(pattern, payload, http.StatusOK)
}

func (m *MockServer) buildHandler(pattern string) func(http.ResponseWriter, *http.Request) {

	handler := func(w http.ResponseWriter, r *http.Request) {

		if response, ok := m.responsesByMethod[r.URL.String()][r.Method]; ok {
			m.sendData(w, r, response.code, response.payload)
			return
		}

		m.defaultHandler(w, r)
	}

	return handler
}

func (m *MockServer) parsePattern(patternWithMethod string) (pattern string, method string) {
	method = http.MethodGet
	res := strings.Split(patternWithMethod, ":")
	if len(res) > 1 {
		method = res[1]
	}
	pattern = res[0]

	return
}

// ResponseWithCode attaches a handler function that returns the given payload
// from requests to the URL pattern along with the specified code
func (m *MockServer) ResponseWithCode(patternWithMethod string, payload string, code int) *MockServer {

	pattern, method := m.parsePattern(patternWithMethod)

	mh, ok := m.responsesByMethod[pattern]
	if !ok {
		m.responsesByMethod[pattern] = map[string]response{}
		m.mux.HandleFunc(pattern, m.buildHandler(pattern))
	}

	if _, ok = mh[method]; ok {
		panic(fmt.Sprintf("Method handler for [%s] %s was already defined", method, pattern))
	}

	m.t.Logf("%s: adding response for [%s] %s", m.name, method, pattern)
	m.responsesByMethod[pattern][method] = response{
		code:    code,
		payload: payload,
	}
	return m
}

// ResponseJSON marshals the JSON object as payload returned by the response
// handler
func (m *MockServer) ResponseJSON(pattern string, payload interface{}) *MockServer {
	content, err := json.Marshal(payload)
	if err != nil {
		m.t.Error(err)
	}
	m.Response(pattern, string(content))
	return m
}

// ErrorResponse attaches a handler function that returns the given
// error code from requests to the URL pattern
func (m *MockServer) ErrorResponse(pattern string, errorCode int) *MockServer {
	m.t.Logf("%s: adding error response handler for %s", m.name, pattern)
	m.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		m.logRequest(r, fmt.Sprintf("%d", errorCode))
		http.Error(w, "An error", errorCode)
	})
	return m
}

// Start runs the server
func (m *MockServer) Start() *MockServer {
	m.server = httptest.NewServer(m.mux)
	//catch all handler
	m.mux.HandleFunc("/", m.defaultHandler)
	return m
}

// Stop closes the server down
func (m *MockServer) Stop() {
	m.server.Close()
}

// AddDefaultResponseJSON adds a default response for the specified pattern
func (m *MockServer) AddDefaultResponseJSON(patternWithVars string, httpMethod string, code int, payload interface{}) *MockServer {
	content, err := json.Marshal(payload)
	if err != nil {
		m.t.Error(err)
	}
	return m.AddDefaultResponse(patternWithVars, httpMethod, code, string(content))
}

// AddDefaultResponse adds a default response for the specified pattern/method.
// It is possible to use variables in the pattern using curly braces, ie `/v1/nodes/{id}/power`
// Pattern variables can be reused in the payload, so that they will be substitued with the actual value when sending the response
// If httpMethod is empty, the response will be applied for any method
func (m *MockServer) AddDefaultResponse(patternWithVars string, httpMethod string, code int, payload string) *MockServer {

	pattern := "^" + regexp.MustCompile("{(.[^}]*)}").ReplaceAllString(patternWithVars, "(?P<$1>.[^/]*)") + "$"
	m.t.Logf("%s: adding default response for %s (%s) -> {%d, %s}", m.name, patternWithVars, pattern, code, payload)

	defaultResponse := defaultResponse{
		re:     regexp.MustCompile(pattern),
		method: httpMethod,
		response: response{
			code:    code,
			payload: payload,
		},
	}

	m.defaultResponses = append(m.defaultResponses, defaultResponse)
	return m
}

func (m *MockServer) defaultHandler(w http.ResponseWriter, r *http.Request) {

	url := r.URL.String()
	method := r.Method

	for _, response := range m.defaultResponses {
		if response.method == "" || response.method == method {
			match := response.re.FindStringSubmatch(url)
			if match == nil {
				continue
			}

			m.t.Logf("%s: found default response for %s: {%d, %s}", m.name, url, response.code, response.payload)
			payload := response.payload
			for i, name := range response.re.SubexpNames() {
				if i != 0 && name != "" {
					payload = strings.ReplaceAll(payload, "{"+name+"}", match[i])
				}
			}

			m.sendData(w, r, response.code, payload)
			return
		}
	}

	m.t.Logf("%s: Cannot find any default response for [%s] %s", m.name, method, url)
	m.logRequest(r, "")
}

func (m *MockServer) sendData(w http.ResponseWriter, r *http.Request, code int, payload string) {
	m.logRequest(r, payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	fmt.Fprint(w, payload)
}
