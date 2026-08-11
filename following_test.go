package twitter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const followingSample = `{
  "data": {
    "user": {
      "result": {
        "timeline": {
          "timeline": {
            "instructions": [
              {
                "type": "TimelineAddEntries",
                "entries": [
                  {"entryId":"user-1","content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":"111","legacy":{"screen_name":"alice","name":"Alice"}}}}}},
                  {"entryId":"user-2","content":{"entryType":"TimelineTimelineItem","itemContent":{"user_results":{"result":{"rest_id":"","legacy":{"screen_name":"","name":""}}}}}},
                  {"entryId":"cursor-top","content":{"entryType":"TimelineTimelineCursor","cursorType":"Top","value":"TOPCUR"}},
                  {"entryId":"cursor-bottom","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"BOTCUR"}}
                ]
              }
            ]
          }
        }
      }
    }
  }
}`

func TestFollowingConfigHelperDefaults(t *testing.T) {
	c := New() // nothing configured: helpers must fall back to the defaults
	if got := c.apiBaseURL(); got != DefaultAPIBaseURL {
		t.Errorf("apiBaseURL default = %q", got)
	}
	if got := c.bearer(); got != DefaultWebBearer {
		t.Errorf("bearer default = %q", got)
	}
	if got := c.followingQueryID(); got != DefaultFollowingQueryID {
		t.Errorf("followingQueryID default = %q", got)
	}
}

func TestFollowingConfigHelperOverrides(t *testing.T) {
	c := New(
		WithAPIBaseURL("https://api.test/"),
		WithBearer("BEAR"),
		WithFollowingQueryID("QID"),
	)
	if got := c.apiBaseURL(); got != "https://api.test" {
		t.Errorf("apiBaseURL override = %q", got)
	}
	if got := c.bearer(); got != "BEAR" {
		t.Errorf("bearer override = %q", got)
	}
	if got := c.followingQueryID(); got != "QID" {
		t.Errorf("followingQueryID override = %q", got)
	}
}

func TestFollowingNeedsAuth(t *testing.T) {
	// No session cookies configured → ErrNeedsAuth, no request made.
	_, err := New().Following(context.Background(), "42", "")
	if !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("err = %v, want ErrNeedsAuth", err)
	}
}

func TestFollowingSuccess(t *testing.T) {
	var gotAuthz, gotCSRF, gotCookie, gotAuthType, gotPath, gotVars string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthz = r.Header.Get("Authorization")
		gotCSRF = r.Header.Get("x-csrf-token")
		gotCookie = r.Header.Get("Cookie")
		gotAuthType = r.Header.Get("x-twitter-auth-type")
		gotPath = r.URL.Path
		gotVars = r.URL.Query().Get("variables")
		_, _ = w.Write([]byte(followingSample))
	}))
	defer srv.Close()

	c := New(
		WithAPIBaseURL(srv.URL),
		WithSessionCookies("AUTHTOK", "CT0TOK"),
		WithBearer("WEBBEARER"),
		WithFollowingQueryID("QID123"),
	)
	page, err := c.Following(context.Background(), "42", "PREVCUR")
	if err != nil {
		t.Fatalf("Following: %v", err)
	}

	if gotAuthz != "Bearer WEBBEARER" {
		t.Errorf("Authorization = %q", gotAuthz)
	}
	if gotCSRF != "CT0TOK" {
		t.Errorf("x-csrf-token = %q", gotCSRF)
	}
	if gotCookie != "auth_token=AUTHTOK; ct0=CT0TOK" {
		t.Errorf("Cookie = %q", gotCookie)
	}
	if gotAuthType != "OAuth2Session" {
		t.Errorf("x-twitter-auth-type = %q", gotAuthType)
	}
	if gotPath != "/i/api/graphql/QID123/Following" {
		t.Errorf("path = %q", gotPath)
	}
	if !strings.Contains(gotVars, `"userId":"42"`) || !strings.Contains(gotVars, "PREVCUR") {
		t.Errorf("variables = %q, want userId + cursor", gotVars)
	}

	if page.Cursor != "BOTCUR" {
		t.Errorf("cursor = %q, want BOTCUR", page.Cursor)
	}
	if len(page.Users) != 1 {
		t.Fatalf("users = %d, want 1 (empty rest_id skipped)", len(page.Users))
	}
	if page.Users[0] != (FollowedUser{ID: "111", ScreenName: "alice", Name: "Alice"}) {
		t.Errorf("user0 = %+v", page.Users[0])
	}
}

func TestFollowingFirstPageNoCursor(t *testing.T) {
	var gotVars string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotVars = r.URL.Query().Get("variables")
		_, _ = w.Write([]byte(`{"data":{}}`))
	}))
	defer srv.Close()

	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	page, err := c.Following(context.Background(), "42", "")
	if err != nil {
		t.Fatalf("Following: %v", err)
	}
	if strings.Contains(gotVars, "cursor") {
		t.Errorf("variables = %q, must not carry a cursor on the first page", gotVars)
	}
	if len(page.Users) != 0 || page.Cursor != "" {
		t.Errorf("page = %+v, want empty", page)
	}
}

func TestFollowingUnauthorized(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
		_, err := c.Following(context.Background(), "42", "")
		srv.Close()
		if !errors.Is(err, ErrNeedsAuth) {
			t.Fatalf("status %d: err = %v, want ErrNeedsAuth", code, err)
		}
	}
}

func TestFollowingQueryIDRotated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if !errors.Is(err, ErrQueryIDRotated) {
		t.Fatalf("err = %v, want ErrQueryIDRotated", err)
	}
}

func TestFollowingGenericStatusLongBody(t *testing.T) {
	long := strings.Repeat("x", 300)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(long))
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if err == nil || !strings.Contains(err.Error(), "status 500") || !strings.Contains(err.Error(), "…") {
		t.Fatalf("expected truncated 500 error, got %v", err)
	}
}

func TestFollowingGenericStatusShortBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("nope"))
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if err == nil || !strings.Contains(err.Error(), "status 502") || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected 502 error carrying body, got %v", err)
	}
}

func TestFollowingDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if err == nil || !strings.Contains(err.Error(), "decode Following") {
		t.Fatalf("expected decode error, got %v", err)
	}
}

func TestFollowingBuildRequestError(t *testing.T) {
	c := New(WithAPIBaseURL("http://\x7f.example"), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("expected build request error, got %v", err)
	}
}

func TestFollowingRequestFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	c := New(WithAPIBaseURL(url), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if err == nil {
		t.Fatalf("expected transport error, got nil")
	}
}

func TestFollowingReadBodyError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "500")
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			_, _ = w.Write([]byte("short"))
			f.Flush()
		}
		if hj, ok := w.(http.Hijacker); ok {
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
		}
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.Following(context.Background(), "42", "")
	if err == nil {
		t.Fatalf("expected read body error, got nil")
	}
}
