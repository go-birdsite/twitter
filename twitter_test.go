package twitter

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const sampleHTML = `<!doctype html><html><body>
<script id="__NEXT_DATA__" type="application/json">
{"props":{"pageProps":{"timeline":{"entries":[
  {"type":"tweet","content":{"tweet":{
    "id_str":"123","full_text":"hello world","created_at":"Wed Oct 10 20:19:24 +0000 2018",
    "favorite_count":5,"retweet_count":2,"reply_count":1,
    "user":{"screen_name":"jack"},
    "entities":{"media":[{"media_url_https":"https://pbs/a.jpg","type":"photo"}]},
    "extended_entities":{"media":[{"media_url_https":"https://pbs/a.jpg","type":"photo"},{"media_url_https":"https://pbs/b.mp4","type":"video"}]}
  }}},
  {"type":"timelineCursor","content":{"tweet":{"id_str":"x"}}},
  {"type":"tweet","content":{"tweet":{"id_str":"456","full_text":"no media","created_at":"bad-date","user":{"screen_name":"jack"}}}}
]}}}}
</script></body></html>`

func serve(t *testing.T, status int, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/srv/timeline-profile/screen-name/") {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return New(WithBaseURL(srv.URL))
}

func TestNewDefaultsAndOptions(t *testing.T) {
	c := New()
	if c.BaseURL != DefaultBaseURL || c.HTTPClient != http.DefaultClient || !strings.Contains(c.UserAgent, "go-birdsite") {
		t.Fatalf("defaults: %+v", c)
	}
	c2 := New(WithBaseURL("http://x/"), WithUserAgent("ua"), WithAuthToken("tok"), WithHTTPClient(&http.Client{Timeout: time.Second}))
	if c2.BaseURL != "http://x" || c2.UserAgent != "ua" || c2.AuthToken != "tok" || c2.HTTPClient.Timeout != time.Second {
		t.Fatalf("options: %+v", c2)
	}
}

func TestHTTPClientFallback(t *testing.T) {
	if (&Client{}).httpClient() != http.DefaultClient {
		t.Fatal("fallback")
	}
}

func TestUserTweets(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		io.WriteString(w, sampleHTML)
	}))
	defer srv.Close()
	c := New(WithBaseURL(srv.URL), WithAuthToken("tok"))

	tl, err := c.UserTweets(context.Background(), "jack")
	if err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer tok" {
		t.Fatalf("auth = %q", gotAuth)
	}
	// Two "tweet" entries; the cursor entry is skipped.
	if len(tl.Tweets) != 2 {
		t.Fatalf("tweets = %d, want 2", len(tl.Tweets))
	}
	a := tl.Tweets[0]
	if a.ID != "123" || a.Text != "hello world" || a.Author != "jack" ||
		a.Permalink != "https://twitter.com/jack/status/123" ||
		a.Likes != 5 || a.Retweets != 2 || a.Replies != 1 {
		t.Fatalf("tweet A = %+v", a)
	}
	if a.CreatedAt.IsZero() {
		t.Fatal("createdAt should parse")
	}
	// Media deduped by URL: a.jpg once + b.mp4 => 2.
	if len(a.Media) != 2 || a.Media[0].URL != "https://pbs/a.jpg" || a.Media[1].Type != "video" {
		t.Fatalf("media = %+v", a.Media)
	}
	b := tl.Tweets[1]
	if b.ID != "456" || !b.CreatedAt.IsZero() || len(b.Media) != 0 {
		t.Fatalf("tweet B = %+v", b)
	}
}

func TestUserTweetsRequestError(t *testing.T) {
	// A control character in the screen name breaks request construction.
	if _, err := New().UserTweets(context.Background(), "bad\x7fname\n"); err == nil {
		t.Fatal("want request build error")
	}
}

func TestUserTweetsTransportError(t *testing.T) {
	c := New(WithHTTPClient(&http.Client{Transport: errRT{}}))
	if _, err := c.UserTweets(context.Background(), "jack"); err == nil {
		t.Fatal("want transport error")
	}
}

func TestUserTweetsBodyReadError(t *testing.T) {
	c := New(WithHTTPClient(&http.Client{Transport: badBodyRT{}}))
	if _, err := c.UserTweets(context.Background(), "jack"); err == nil {
		t.Fatal("want body read error")
	}
}

func TestUserTweetsStatusError(t *testing.T) {
	c := serve(t, http.StatusForbidden, "blocked")
	if _, err := c.UserTweets(context.Background(), "jack"); err == nil {
		t.Fatal("want 403 error")
	}
}

func TestUserTweetsNoNextData(t *testing.T) {
	c := serve(t, 200, "<html>no next data here</html>")
	if _, err := c.UserTweets(context.Background(), "jack"); err == nil {
		t.Fatal("want __NEXT_DATA__ not-found error")
	}
}

func TestUserTweetsBadJSON(t *testing.T) {
	c := serve(t, 200, `<script id="__NEXT_DATA__" type="application/json">{not json}</script>`)
	if _, err := c.UserTweets(context.Background(), "jack"); err == nil {
		t.Fatal("want decode error")
	}
}

func TestExtractNextData(t *testing.T) {
	if _, err := extractNextData("<html>no script</html>"); err == nil {
		t.Fatal("want not-found error")
	}
	// Opening tag never closes.
	if _, err := extractNextData(`<script id="__NEXT_DATA__"`); err == nil {
		t.Fatal("want malformed-tag error")
	}
	// No closing </script>.
	if _, err := extractNextData(`<script id="__NEXT_DATA__">{"a":1}`); err == nil {
		t.Fatal("want unterminated error")
	}
	got, err := extractNextData(`x<script id="__NEXT_DATA__" type="application/json"> {"a":1} </script>y`)
	if err != nil || got != `{"a":1}` {
		t.Fatalf("extract = %q, %v", got, err)
	}
}

type errRT struct{}

func (errRT) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("boom") }

type badBodyRT struct{}

func (badBodyRT) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(errReader{}), Header: make(http.Header)}, nil
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("read boom") }
