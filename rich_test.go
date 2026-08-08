package twitter

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
)

// page wraps a __NEXT_DATA__ payload in the surrounding HTML the endpoint serves.
func page(json string) string {
	return `<!doctype html><html><body><script id="__NEXT_DATA__" type="application/json">` +
		json + `</script></body></html>`
}

// timelinePage wraps entries JSON into a full pageProps document.
func timelinePage(entries string) string {
	return page(`{"props":{"pageProps":{"timeline":{"entries":[` + entries + `]}}}}`)
}

const richTweet = `{"type":"tweet","content":{"tweet":{
  "id_str":"1","full_text":"look https://t.co/AAA and https://t.co/BBB",
  "created_at":"Thu Aug 06 20:26:47 +0000 2026",
  "favorite_count":10,"retweet_count":3,"reply_count":2,"quote_count":7,
  "lang":"en","possibly_sensitive":true,
  "user":{"id_str":"11","screen_name":"NASA","name":"NASA","profile_image_url_https":"https://pbs/av.jpg",
          "description":"space","is_blue_verified":true,"followers_count":1234,"protected":false},
  "entities":{"urls":[
     {"url":"https://t.co/AAA","expanded_url":"https://x.com/NASA/status/1/photo/1","display_url":"pic.x.com/AAA"},
     {"url":"https://t.co/BBB","expanded_url":"http://nasa.gov/live","display_url":"nasa.gov/live"},
     {"url":"","expanded_url":"http://dropped.example"}]},
  "extended_entities":{"media":[{
     "media_url_https":"https://pbs/thumb.jpg","type":"video","ext_alt_text":"a rocket",
     "original_info":{"width":1280,"height":720},
     "video_info":{"duration_millis":103336,"variants":[
        {"url":"https://v/hls.m3u8","content_type":"application/x-mpegURL"},
        {"url":"https://v/low.mp4","content_type":"video/mp4","bitrate":288000},
        {"url":"https://v/high.mp4","content_type":"video/mp4","bitrate":2176000}]}}]}
}}}`

func TestUserTweetsRichFields(t *testing.T) {
	c := serve(t, 200, timelinePage(richTweet))
	tl, err := c.UserTweets(context.Background(), "NASA")
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Tweets) != 1 {
		t.Fatalf("tweets = %d", len(tl.Tweets))
	}
	tw := tl.Tweets[0]

	if tw.Quotes != 7 || tw.Lang != "en" || !tw.Sensitive {
		t.Fatalf("scalars = %+v", tw)
	}
	want := User{ID: "11", ScreenName: "NASA", Name: "NASA", AvatarURL: "https://pbs/av.jpg",
		Description: "space", Verified: true, Followers: 1234}
	if tw.User != want {
		t.Fatalf("user = %+v, want %+v", tw.User, want)
	}
	// The empty-URL entity is dropped; the other two survive in order.
	if len(tw.Links) != 2 || tw.Links[1] != (Link{URL: "https://t.co/BBB", Expanded: "http://nasa.gov/live", Display: "nasa.gov/live"}) {
		t.Fatalf("links = %+v", tw.Links)
	}
	if len(tw.Media) != 1 {
		t.Fatalf("media = %+v", tw.Media)
	}
	m := tw.Media[0]
	if m.AltText != "a rocket" || m.Width != 1280 || m.Height != 720 || m.DurationMS != 103336 || len(m.Variants) != 3 {
		t.Fatalf("media = %+v", m)
	}
	if m.Variants[0] != (VideoVariant{URL: "https://v/hls.m3u8", ContentType: "application/x-mpegURL"}) {
		t.Fatalf("variant0 = %+v", m.Variants[0])
	}
}

func TestVerifiedFromLegacyFlag(t *testing.T) {
	// verified (legacy) alone also marks the user verified.
	if u := mapUser(rawUser{Verified: true}); !u.Verified {
		t.Fatal("legacy verified should count")
	}
	if u := mapUser(rawUser{}); u.Verified {
		t.Fatal("unverified user should not be marked")
	}
}

func TestBestVariant(t *testing.T) {
	video := Media{Variants: []VideoVariant{
		{URL: "https://v/hls.m3u8", ContentType: "application/x-mpegURL"},
		{URL: "https://v/low.mp4", ContentType: "video/mp4", Bitrate: 288000},
		{URL: "https://v/high.mp4", ContentType: "video/mp4", Bitrate: 2176000},
	}}
	v, ok := video.BestVariant()
	if !ok || v.URL != "https://v/high.mp4" || v.Bitrate != 2176000 {
		t.Fatalf("best = %+v, ok=%v", v, ok)
	}
	// A photo has no variants at all.
	if _, ok := (Media{Type: "photo"}).BestVariant(); ok {
		t.Fatal("photo should have no playable variant")
	}
	// HLS-only, and an MP4 with no URL, are both unplayable here.
	hls := Media{Variants: []VideoVariant{
		{URL: "https://v/hls.m3u8", ContentType: "application/x-mpegURL"},
		{URL: "", ContentType: "video/mp4", Bitrate: 999},
	}}
	if _, ok := hls.BestVariant(); ok {
		t.Fatal("HLS-only should report no MP4 variant")
	}
}

func TestExpandedTextAndPrimaryLink(t *testing.T) {
	tw := Tweet{
		Text: "look https://t.co/AAA and https://t.co/BBB",
		Links: []Link{
			{URL: "https://t.co/AAA", Expanded: "https://x.com/NASA/status/1/photo/1"},
			{URL: "https://t.co/BBB", Expanded: "http://nasa.gov/live"},
			{URL: "https://t.co/CCC"}, // no expansion known: left as-is
		},
	}
	const wantText = "look https://x.com/NASA/status/1/photo/1 and http://nasa.gov/live"
	if got := tw.ExpandedText(); got != wantText {
		t.Fatalf("expanded = %q", got)
	}
	// The x.com self-link is skipped; the article link wins.
	if got := tw.PrimaryLink(); got != "http://nasa.gov/live" {
		t.Fatalf("primary = %q", got)
	}
	// Nothing but self-links means no external destination.
	self := Tweet{Links: []Link{{URL: "https://t.co/A", Expanded: "https://twitter.com/x/status/2"}}}
	if got := self.PrimaryLink(); got != "" {
		t.Fatalf("primary = %q, want empty", got)
	}
	if got := (Tweet{}).PrimaryLink(); got != "" {
		t.Fatalf("primary of empty = %q", got)
	}
}

func TestIsTwitterHost(t *testing.T) {
	for _, u := range []string{
		"https://twitter.com/a", "http://x.com/a", "https://www.twitter.com/a",
		"https://mobile.twitter.com/a", "x.com/a",
	} {
		if !isTwitterHost(u) {
			t.Fatalf("%q should be a Twitter host", u)
		}
	}
	for _, u := range []string{
		"https://nasa.gov/live", "https://notx.com/a", "https://example.com/x.com/a", "",
	} {
		if isTwitterHost(u) {
			t.Fatalf("%q should not be a Twitter host", u)
		}
	}
}

func TestOriginalPrefersRetweetedTweet(t *testing.T) {
	inner := Tweet{ID: "inner", Text: "the real content"}
	rt := Tweet{ID: "outer", Text: "RT @a: the real…", Retweeted: &inner}
	if got := rt.Original(); got.ID != "inner" {
		t.Fatalf("original = %+v", got)
	}
	plain := Tweet{ID: "solo"}
	if got := plain.Original(); got.ID != "solo" {
		t.Fatalf("original = %+v", got)
	}
}

func TestRetweetAndQuoteMapping(t *testing.T) {
	const entries = `{"type":"tweet","content":{"tweet":{
      "id_str":"outer","full_text":"RT @orig: hi","user":{"screen_name":"rt"},
      "retweeted_status":{"id_str":"inner","full_text":"hi","user":{"screen_name":"orig"}},
      "quoted_status":{"id_str":"q1","full_text":"quoted","user":{"screen_name":"qa"}}}}}`
	c := serve(t, 200, timelinePage(entries))
	tl, err := c.UserTweets(context.Background(), "rt")
	if err != nil {
		t.Fatal(err)
	}
	tw := tl.Tweets[0]
	if tw.Retweeted == nil || tw.Retweeted.ID != "inner" || tw.Retweeted.Author != "orig" {
		t.Fatalf("retweeted = %+v", tw.Retweeted)
	}
	if tw.Quoted == nil || tw.Quoted.ID != "q1" {
		t.Fatalf("quoted = %+v", tw.Quoted)
	}
	if got := tw.Original().Text; got != "hi" {
		t.Fatalf("original text = %q", got)
	}
}

func TestQuotedTweetFallbackKey(t *testing.T) {
	// Some payloads name the field "quoted_tweet" instead of "quoted_status".
	const entries = `{"type":"tweet","content":{"tweet":{
      "id_str":"o","user":{"screen_name":"a"},
      "quoted_tweet":{"id_str":"q2","full_text":"alt key","user":{"screen_name":"b"}}}}}`
	c := serve(t, 200, timelinePage(entries))
	tl, err := c.UserTweets(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if q := tl.Tweets[0].Quoted; q == nil || q.ID != "q2" {
		t.Fatalf("quoted = %+v", q)
	}
}

func TestNestingIsDepthCapped(t *testing.T) {
	// Four levels of retweet: the chain is followed maxNestDepth times, then cut.
	inner := `{"id_str":"d4","user":{"screen_name":"d"}}`
	for i := 3; i >= 1; i-- {
		inner = fmt.Sprintf(`{"id_str":"d%d","user":{"screen_name":"d"},"retweeted_status":%s}`, i, inner)
	}
	c := serve(t, 200, timelinePage(`{"type":"tweet","content":{"tweet":`+inner+`}}`))
	tl, err := c.UserTweets(context.Background(), "d")
	if err != nil {
		t.Fatal(err)
	}
	tw := &tl.Tweets[0]
	depth := 0
	for tw.Retweeted != nil {
		depth++
		tw = tw.Retweeted
	}
	if depth != maxNestDepth {
		t.Fatalf("followed %d levels, want %d", depth, maxNestDepth)
	}
	if tw.ID != "d"+fmt.Sprint(maxNestDepth+1) {
		t.Fatalf("deepest = %q", tw.ID)
	}
}

func TestStatusErrorClassification(t *testing.T) {
	cases := []struct {
		code   int
		target error
	}{
		{http.StatusTooManyRequests, ErrFingerprinted},
		{http.StatusNotFound, ErrNotFound},
		{http.StatusUnauthorized, ErrProtected},
		{http.StatusForbidden, ErrProtected},
	}
	for _, tc := range cases {
		err := statusError("jack", tc.code)
		if !errors.Is(err, tc.target) {
			t.Fatalf("status %d = %v, want %v", tc.code, err, tc.target)
		}
	}
	other := statusError("jack", http.StatusInternalServerError)
	for _, sentinel := range []error{ErrFingerprinted, ErrNotFound, ErrProtected} {
		if errors.Is(other, sentinel) {
			t.Fatalf("500 wrongly classified as %v", sentinel)
		}
	}
	if other == nil {
		t.Fatal("500 must still be an error")
	}
}

func TestUserTweetsFingerprintRefusal(t *testing.T) {
	c := serve(t, http.StatusTooManyRequests, "")
	_, err := c.UserTweets(context.Background(), "jack")
	if !errors.Is(err, ErrFingerprinted) {
		t.Fatalf("err = %v, want ErrFingerprinted", err)
	}
}

func TestUserTweetsProtectedAccount(t *testing.T) {
	// The page renders, headed by a protected profile, with an empty timeline.
	body := page(`{"props":{"pageProps":{"timeline":{"entries":[]},
        "headerProps":{"user":{"screen_name":"secret","protected":true}}}}}`)
	c := serve(t, 200, body)
	_, err := c.UserTweets(context.Background(), "secret")
	if !errors.Is(err, ErrProtected) {
		t.Fatalf("err = %v, want ErrProtected", err)
	}
}

func TestUserTweetsEmptyTimelineIsNotAnError(t *testing.T) {
	// A public account that simply has no tweets is a valid, empty result.
	body := page(`{"props":{"pageProps":{"timeline":{"entries":[]},
        "headerProps":{"user":{"screen_name":"quiet"}}}}}`)
	c := serve(t, 200, body)
	tl, err := c.UserTweets(context.Background(), "quiet")
	if err != nil || len(tl.Tweets) != 0 {
		t.Fatalf("tl = %+v, err = %v", tl, err)
	}
}
