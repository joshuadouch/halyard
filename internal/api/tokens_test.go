package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestAPITokens(t *testing.T) {
	c := newTestServer(t)
	if code, out := c.do("POST", "/api/setup", map[string]string{"username": "admin", "password": "a-long-password"}, false); code != 200 {
		t.Fatalf("setup: %d %v", code, out)
	}
	code, out := c.do("POST", "/api/tokens", map[string]string{"name": "berth"}, true)
	if code != 200 {
		t.Fatalf("create token: %d %v", code, out)
	}
	tok := out["token"].(string)

	bearer := func(method, path, token string, body any) int {
		var b bytes.Buffer
		if body != nil {
			_ = json.NewEncoder(&b).Encode(body)
		}
		req, _ := http.NewRequest(method, c.base+path, &b)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		hc := &http.Client{Transport: c.hc.Transport} // no cookies
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if code := bearer("GET", "/api/hosts", tok, nil); code != 200 {
		t.Fatalf("token read: %d", code)
	}
	// Tokens may change config without the browser-only X-Halyard header.
	vip := map[string]any{"address": "127.0.0.11/8", "label": "t", "interface": "lo", "vrid": 52}
	if code := bearer("POST", "/api/vips", tok, vip); code != 200 {
		t.Fatalf("token write: %d", code)
	}
	if code := bearer("GET", "/api/hosts", "hlt_wrong", nil); code != 401 {
		t.Fatalf("wrong token: %d", code)
	}
	for _, p := range []string{"/api/users", "/api/tokens", "/api/me"} {
		if code := bearer("GET", p, tok, nil); code != 403 {
			t.Fatalf("token reached %s: %d", p, code)
		}
	}
	code, list := c.do("GET", "/api/tokens", nil, false)
	if code != 200 || bytes.Contains([]byte(list["_raw"].(string)), []byte(tok)) {
		t.Fatalf("list: %d %v", code, list)
	}
	var ts []map[string]any
	_ = json.Unmarshal([]byte(list["_raw"].(string)), &ts)
	if len(ts) != 1 || ts[0]["hash"] != "" {
		t.Fatalf("list leaks or is wrong: %v", ts)
	}
	if code, _ := c.do("DELETE", "/api/tokens/"+ts[0]["id"].(string), nil, true); code != 200 {
		t.Fatal("revoke failed")
	}
	if code := bearer("GET", "/api/hosts", tok, nil); code != 401 {
		t.Fatalf("revoked token still works: %d", code)
	}
}
