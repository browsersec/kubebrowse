package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContextJSON(t *testing.T) {
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/sessions/example", nil)
	context := NewContext(recorder, request, map[string]string{"connectionID": "example"})

	context.JSON(http.StatusCreated, H{"id": context.Param("connectionID")})

	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusCreated)
	}
	var response map[string]string
	if err := json.NewDecoder(recorder.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["id"] != "example" {
		t.Fatalf("response id = %q, want %q", response["id"], "example")
	}
}

func TestContextFormFileReturnsReusableHeader(t *testing.T) {
	body := strings.NewReader("--boundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"example.txt\"\r\nContent-Type: text/plain\r\n\r\ncontents\r\n--boundary--\r\n")
	request := httptest.NewRequest(http.MethodPost, "/upload", body)
	request.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	context := NewContext(httptest.NewRecorder(), request, nil)

	header, err := context.FormFile("file")
	if err != nil {
		t.Fatalf("FormFile: %v", err)
	}
	file, err := header.Open()
	if err != nil {
		t.Fatalf("reopen file header: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close reopened file: %v", err)
	}
}
