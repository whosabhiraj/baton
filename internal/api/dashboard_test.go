package api

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestStaticDashboard(t *testing.T) {
	if StaticDashboard(fstest.MapFS{}) != nil {
		t.Fatal("no index.html must give nil")
	}
	h := StaticDashboard(fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("js")},
	})
	get := func(p string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		b, _ := io.ReadAll(w.Result().Body)
		return w.Code, string(b)
	}
	for _, p := range []string{"/", "/runs/abc"} {
		if c, b := get(p); c != 200 || !strings.Contains(b, "app") {
			t.Fatalf("%s: %d %q", p, c, b)
		}
	}
	if c, b := get("/assets/app.js"); c != 200 || b != "js" {
		t.Fatalf("asset: %d %q", c, b)
	}
	if c, _ := get("/assets/missing.js"); c != 404 {
		t.Fatalf("missing asset: %d", c)
	}
}
