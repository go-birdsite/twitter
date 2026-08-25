package twitter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// tweetResult builds one GraphQL tweet_results.result object. When rt or quote is
// non-empty it is embedded as the retweeted / quoted nested result.
func tweetResult(restID, screenName, text, mediaURL, mediaType, rt, quote string) string {
	media := ""
	if mediaURL != "" {
		media = `,"extended_entities":{"media":[{"media_url_https":"` + mediaURL +
			`","type":"` + mediaType + `","original_info":{"width":100,"height":80}` +
			`,"video_info":{"duration_millis":1000,"variants":[{"url":"https://v/x.mp4","content_type":"video/mp4","bitrate":256}]}}]}`
	}
	nested := ""
	if rt != "" {
		nested += `,"retweeted_status_result":{"result":` + rt + `}`
	}
	if quote != "" {
		nested += `,"quoted_status_result":{"result":` + quote + `}`
	}
	return `{"rest_id":"` + restID + `",` +
		`"core":{"user_results":{"result":{"rest_id":"9` + restID + `","legacy":{"screen_name":"` + screenName + `","name":"Name"}}}},` +
		`"legacy":{"id_str":"` + restID + `","full_text":"` + text + `","created_at":"Wed Oct 10 20:19:24 +0000 2018","favorite_count":3` + media + nested + `}}`
}

// userTweetsJSON assembles a full UserTweets timeline response: a pinned tweet,
// then an add-entries instruction carrying a photo tweet, a video tweet, a
// visibility-wrapped tweet, a retweet, a quote, a self-thread module and a cursor.
func userTweetsJSON() string {
	photo := tweetResult("1", "nasa", "photo", "https://pbs.twimg.com/media/a.jpg", "photo", "", "")
	video := tweetResult("2", "nasa", "video", "https://pbs.twimg.com/media/v.jpg", "video", "", "")
	// A tweet whose own legacy has no id_str and whose author legacy has no id_str,
	// exercising the rest_id / core-rest_id fallbacks.
	noIDs := `{"rest_id":"3","core":{"user_results":{"result":{"rest_id":"93","legacy":{"screen_name":"nasa","name":"Name"}}}},"legacy":{"full_text":"noid","created_at":"Wed Oct 10 20:19:24 +0000 2018"}}`
	twvr := `{"__typename":"TweetWithVisibilityResults","tweet":` + tweetResult("4", "nasa", "wrapped", "", "", "", "") + `}`
	rtOriginal := tweetResult("50", "orig", "the original", "https://pbs.twimg.com/media/rt.jpg", "photo", "", "")
	retweet := tweetResult("5", "nasa", "RT @orig: the original", "", "", rtOriginal, "")
	qtOriginal := tweetResult("60", "quoted", "quoted body", "", "", "", "")
	quote := tweetResult("6", "nasa", "look at this", "", "", "", qtOriginal)
	module := tweetResult("7", "nasa", "thread head", "", "", "", "")
	pinned := tweetResult("8", "nasa", "pinned", "", "", "", "")

	entries := `[` +
		`{"entryId":"tweet-1","content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + photo + `}}}},` +
		`{"entryId":"tweet-2","content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + video + `}}}},` +
		`{"entryId":"tweet-3","content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + noIDs + `}}}},` +
		`{"entryId":"tweet-4","content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + twvr + `}}}},` +
		`{"entryId":"tweet-5","content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + retweet + `}}}},` +
		`{"entryId":"tweet-6","content":{"entryType":"TimelineTimelineItem","itemContent":{"tweet_results":{"result":` + quote + `}}}},` +
		`{"entryId":"profile-conversation-9","content":{"entryType":"TimelineTimelineModule","items":[{"item":{"itemContent":{"tweet_results":{"result":` + module + `}}}}]}},` +
		`{"entryId":"cursor-bottom-0","content":{"entryType":"TimelineTimelineCursor","cursorType":"Bottom","value":"CURSOR"}}` +
		`]`
	pin := `{"type":"TimelinePinEntry","entry":{"entryId":"tweet-8","content":{"itemContent":{"tweet_results":{"result":` + pinned + `}}}}}`
	add := `{"type":"TimelineAddEntries","entries":` + entries + `}`
	return `{"data":{"user":{"result":{"__typename":"User","timeline":{"timeline":{"instructions":[` + pin + `,` + add + `]}}}}}}`
}

// authServer routes UserByScreenName / UserTweets to canned bodies (status 200),
// so a UserTweetsAuth call exercises both halves.
func authServer(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "UserByScreenName"):
			_, _ = w.Write([]byte(`{"data":{"user":{"result":{"rest_id":"11348282"}}}}`))
		case strings.Contains(r.URL.Path, "UserTweets"):
			_, _ = w.Write([]byte(userTweetsJSON()))
		default:
			http.Error(w, "unexpected", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return New(WithAPIBaseURL(srv.URL), WithSessionCookies("AUTHTOK", "CT0TOK"))
}

func TestAuthTimelineConfigDefaults(t *testing.T) {
	c := New()
	if c.userByScreenNameQueryID() != DefaultUserByScreenNameQueryID {
		t.Errorf("UserByScreenName id = %q", c.userByScreenNameQueryID())
	}
	if c.userTweetsQueryID() != DefaultUserTweetsQueryID {
		t.Errorf("UserTweets id = %q", c.userTweetsQueryID())
	}
}

func TestAuthTimelineConfigOverrides(t *testing.T) {
	c := New(WithUserByScreenNameQueryID("UBS"), WithUserTweetsQueryID("UT"))
	if c.userByScreenNameQueryID() != "UBS" {
		t.Errorf("UserByScreenName override = %q", c.userByScreenNameQueryID())
	}
	if c.userTweetsQueryID() != "UT" {
		t.Errorf("UserTweets override = %q", c.userTweetsQueryID())
	}
}

func TestUserTweetsAuthSuccess(t *testing.T) {
	c := authServer(t)
	tl, err := c.UserTweetsAuth(context.Background(), "NASA")
	if err != nil {
		t.Fatalf("UserTweetsAuth: %v", err)
	}
	// pinned + photo + video + noIDs + wrapped + retweet + quote + module = 8; cursor skipped.
	if len(tl.Tweets) != 8 {
		t.Fatalf("got %d tweets, want 8", len(tl.Tweets))
	}
	byID := map[string]Tweet{}
	for _, tw := range tl.Tweets {
		byID[tw.ID] = tw
	}
	if m := byID["1"].Media; len(m) != 1 || m[0].URL != "https://pbs.twimg.com/media/a.jpg" || m[0].Type != "photo" {
		t.Errorf("photo tweet media = %+v", m)
	}
	if v, ok := byID["2"].Media[0].BestVariant(); !ok || v.URL != "https://v/x.mp4" {
		t.Errorf("video tweet best variant = %+v ok=%v", v, ok)
	}
	if byID["3"].ID != "3" || byID["3"].Author != "nasa" {
		t.Errorf("rest_id / core fallback tweet = %+v", byID["3"])
	}
	if byID["4"].Text != "wrapped" {
		t.Errorf("visibility-wrapped tweet text = %q", byID["4"].Text)
	}
	if rt := byID["5"].Retweeted; rt == nil || rt.Text != "the original" {
		t.Errorf("retweet original = %+v", rt)
	}
	if q := byID["6"].Quoted; q == nil || q.Text != "quoted body" {
		t.Errorf("quoted tweet = %+v", q)
	}
	if byID["7"].Text != "thread head" {
		t.Errorf("module tweet missing: %+v", byID["7"])
	}
	if byID["8"].Text != "pinned" {
		t.Errorf("pinned tweet missing: %+v", byID["8"])
	}
}

func TestUserIDByScreenNameNeedsAuth(t *testing.T) {
	_, err := New().UserIDByScreenName(context.Background(), "nasa")
	if !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("want ErrNeedsAuth, got %v", err)
	}
}

func TestUserTweetsByIDNeedsAuth(t *testing.T) {
	_, err := New().UserTweetsByID(context.Background(), "1")
	if !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("want ErrNeedsAuth, got %v", err)
	}
}

func TestUserTweetsAuthNeedsAuthPropagates(t *testing.T) {
	_, err := New().UserTweetsAuth(context.Background(), "nasa")
	if !errors.Is(err, ErrNeedsAuth) {
		t.Fatalf("want ErrNeedsAuth, got %v", err)
	}
}

func TestUserIDByScreenNameNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"user":{"result":{}}}}`))
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.UserIDByScreenName(context.Background(), "ghost")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestUserIDByScreenNameDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.UserIDByScreenName(context.Background(), "nasa")
	if err == nil || !strings.Contains(err.Error(), "decode UserByScreenName") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestUserTweetsByIDDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.UserTweetsByID(context.Background(), "1")
	if err == nil || !strings.Contains(err.Error(), "decode UserTweets") {
		t.Fatalf("want decode error, got %v", err)
	}
}

func TestUserTweetsByIDStatusError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
	_, err := c.UserTweetsByID(context.Background(), "1")
	if !errors.Is(err, ErrQueryIDRotated) {
		t.Fatalf("want ErrQueryIDRotated, got %v", err)
	}
}

func TestAuthGraphGetStatusErrors(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		check  func(error) bool
	}{
		{"unauthorized", http.StatusUnauthorized, ``, func(e error) bool { return errors.Is(e, ErrNeedsAuth) }},
		{"forbidden", http.StatusForbidden, ``, func(e error) bool { return errors.Is(e, ErrNeedsAuth) }},
		{"rotated", http.StatusNotFound, ``, func(e error) bool { return errors.Is(e, ErrQueryIDRotated) }},
		{"features", http.StatusBadRequest, strings.Repeat("x", 300), func(e error) bool { return strings.Contains(e.Error(), "…") }},
		{"short", http.StatusInternalServerError, "boom", func(e error) bool { return strings.Contains(e.Error(), "boom") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c := New(WithAPIBaseURL(srv.URL), WithSessionCookies("a", "b"))
			_, err := c.UserIDByScreenName(context.Background(), "nasa")
			if err == nil || !tc.check(err) {
				t.Fatalf("%s: got %v", tc.name, err)
			}
		})
	}
}

func TestAuthGraphGetBuildRequestError(t *testing.T) {
	c := New(WithAPIBaseURL("http://\x7f.example"), WithSessionCookies("a", "b"))
	_, err := c.UserIDByScreenName(context.Background(), "nasa")
	if err == nil || !strings.Contains(err.Error(), "build request") {
		t.Fatalf("want build request error, got %v", err)
	}
}

func TestAuthGraphGetRequestFailed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // connection refused
	c := New(WithAPIBaseURL(url), WithSessionCookies("a", "b"))
	if _, err := c.UserIDByScreenName(context.Background(), "nasa"); err == nil {
		t.Fatal("want transport error, got nil")
	}
}

func TestAuthGraphGetReadBodyError(t *testing.T) {
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
	if _, err := c.UserIDByScreenName(context.Background(), "nasa"); err == nil {
		t.Fatal("want read body error, got nil")
	}
}

func TestMapGraphTweetNilAndEmpty(t *testing.T) {
	if _, ok := mapGraphTweet(nil, 0); ok {
		t.Error("nil result should map to ok=false")
	}
	if _, ok := mapGraphTweet(&graphTweetResult{}, 0); ok {
		t.Error("empty result (no id) should map to ok=false")
	}
}
