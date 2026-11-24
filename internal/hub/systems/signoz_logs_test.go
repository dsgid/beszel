package systems

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEscapeFilter(t *testing.T) {
	assert.Equal(t, `a\'b\\c`, escapeFilter(`a'b\c`))
}

func TestLogRowsAndFormat(t *testing.T) {
	raw := []byte(`{"status":"success","data":{"type":"raw","data":{"results":[{"queryName":"A","rows":[
		{"timestamp":"2026-11-24T06:00:02Z","data":{"body":"second\n"}},
		{"data":{"timestamp":"2026-11-24T06:00:01Z","body":"first"}}]}]}}}`)
	lines, err := logRows(raw)
	require.NoError(t, err)
	require.Len(t, lines, 2)
	// the query returns newest first, the text reads oldest first
	assert.Equal(t, "2026-11-24T06:00:01Z first\n2026-11-24T06:00:02Z second\n", formatLogs(lines))

	empty, err := logRows([]byte(`{"data":{"data":{"results":[]}}}`))
	require.NoError(t, err)
	assert.Empty(t, empty)
	_, err = logRows([]byte(`not json`))
	assert.Error(t, err)
}

func TestSignozLogsRequest(t *testing.T) {
	var got map[string]any
	var key string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key = r.Header.Get("SIGNOZ-API-KEY")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		_, _ = w.Write([]byte(`{"data":{"data":{"results":[{"rows":[{"timestamp":"t","data":{"body":"hello"}}]}]}}}`))
	}))
	defer srv.Close()
	s := &signozSource{url: srv.URL, key: "k", client: srv.Client(), now: func() time.Time { return time.Unix(1800000000, 0) }}
	out, err := s.logs(context.Background(), "resource.host.name = 'h'")
	require.NoError(t, err)
	assert.Equal(t, "t hello\n", out)
	assert.Equal(t, "k", key)
	assert.Equal(t, "raw", got["requestType"])
	spec := got["compositeQuery"].(map[string]any)["queries"].([]any)[0].(map[string]any)["spec"].(map[string]any)
	assert.Equal(t, "logs", spec["signal"])
	assert.Equal(t, "resource.host.name = 'h'", spec["filter"].(map[string]any)["expression"])
	assert.EqualValues(t, signozLogLimit, spec["limit"])
}

func TestSignozLogsHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad filter", http.StatusBadRequest)
	}))
	defer srv.Close()
	s := &signozSource{url: srv.URL, key: "k", client: srv.Client(), now: time.Now}
	_, err := s.logs(context.Background(), "x")
	assert.ErrorContains(t, err, "400")
}
