// Package twitter is a dependency-free, best-effort read client for public
// Twitter/X profile timelines. It uses the public syndication timeline endpoint
// that powers embedded timeline widgets, extracting the tweets from the
// __NEXT_DATA__ JSON blob in the returned HTML.
//
// This is inherently fragile: Twitter/X changes and locks these endpoints, and
// some profiles or rate states require a valid auth token. Requests that are
// blocked surface as errors rather than pretending to be reliable. Expect to
// need WithAuthToken for anything beyond light public reads.
package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the public syndication host.
const DefaultBaseURL = "https://syndication.twitter.com"

// twitterTimeLayout is Twitter's created_at format.
const twitterTimeLayout = "Mon Jan 02 15:04:05 -0700 2006"

// Client reads public profile timelines.
type Client struct {
	// BaseURL is the syndication host; defaults to DefaultBaseURL.
	BaseURL string
	// HTTPClient is used for all requests; defaults to http.DefaultClient.
	HTTPClient *http.Client
	// UserAgent is sent with every request.
	UserAgent string
	// AuthToken, when set, is sent as a bearer token for authenticated reads.
	AuthToken string
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient sets the http.Client used for requests.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.HTTPClient = h } }

// WithBaseURL overrides the syndication host (used in tests).
func WithBaseURL(u string) Option { return func(c *Client) { c.BaseURL = strings.TrimRight(u, "/") } }

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.UserAgent = ua } }

// WithAuthToken sets an optional bearer token for authenticated reads.
func WithAuthToken(t string) Option { return func(c *Client) { c.AuthToken = t } }

// New returns a Client with sane defaults.
func New(opts ...Option) *Client {
	c := &Client{
		BaseURL:    DefaultBaseURL,
		HTTPClient: http.DefaultClient,
		UserAgent:  "Mozilla/5.0 (compatible; go-birdsite/twitter)",
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// Media is an attachment on a tweet.
type Media struct {
	URL  string
	Type string // "photo" | "video" | "animated_gif"
}

// Tweet is a single normalized tweet.
type Tweet struct {
	ID        string
	Text      string
	Author    string // screen name
	Permalink string
	CreatedAt time.Time
	Likes     int
	Retweets  int
	Replies   int
	Media     []Media
}

// Timeline is a page of tweets.
type Timeline struct {
	Tweets []Tweet
}

// UserTweets fetches the public profile timeline for screenName.
func (c *Client) UserTweets(ctx context.Context, screenName string) (*Timeline, error) {
	u := c.BaseURL + "/srv/timeline-profile/screen-name/" + screenName + "?showReplies=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "text/html")
	if c.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.AuthToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("twitter: GET %s: unexpected status %d", screenName, resp.StatusCode)
	}

	raw, err := extractNextData(string(body))
	if err != nil {
		return nil, err
	}

	var doc nextData
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return nil, fmt.Errorf("twitter: decode __NEXT_DATA__: %w", err)
	}

	tl := &Timeline{}
	for _, e := range doc.Props.PageProps.Timeline.Entries {
		if e.Type != "tweet" {
			continue
		}
		tl.Tweets = append(tl.Tweets, mapTweet(e.Content.Tweet))
	}
	return tl, nil
}

// extractNextData returns the JSON inside the __NEXT_DATA__ script tag.
func extractNextData(html string) (string, error) {
	const marker = `id="__NEXT_DATA__"`
	i := strings.Index(html, marker)
	if i < 0 {
		return "", fmt.Errorf("twitter: __NEXT_DATA__ not found")
	}
	// Move to the end of the opening <script ...> tag.
	open := strings.IndexByte(html[i:], '>')
	if open < 0 {
		return "", fmt.Errorf("twitter: malformed __NEXT_DATA__ script tag")
	}
	start := i + open + 1
	end := strings.Index(html[start:], "</script>")
	if end < 0 {
		return "", fmt.Errorf("twitter: unterminated __NEXT_DATA__ script tag")
	}
	return strings.TrimSpace(html[start : start+end]), nil
}

type nextData struct {
	Props struct {
		PageProps struct {
			Timeline struct {
				Entries []entry `json:"entries"`
			} `json:"timeline"`
		} `json:"pageProps"`
	} `json:"props"`
}

type entry struct {
	Type    string `json:"type"`
	Content struct {
		Tweet rawTweet `json:"tweet"`
	} `json:"content"`
}

type rawTweet struct {
	IDStr         string `json:"id_str"`
	FullText      string `json:"full_text"`
	CreatedAt     string `json:"created_at"`
	FavoriteCount int    `json:"favorite_count"`
	RetweetCount  int    `json:"retweet_count"`
	ReplyCount    int    `json:"reply_count"`
	User          struct {
		ScreenName string `json:"screen_name"`
	} `json:"user"`
	Entities struct {
		Media []rawMedia `json:"media"`
	} `json:"entities"`
	ExtendedEntities struct {
		Media []rawMedia `json:"media"`
	} `json:"extended_entities"`
}

type rawMedia struct {
	MediaURLHTTPS string `json:"media_url_https"`
	Type          string `json:"type"`
}

func mapTweet(t rawTweet) Tweet {
	created, _ := time.Parse(twitterTimeLayout, t.CreatedAt) // zero time on failure
	tw := Tweet{
		ID:        t.IDStr,
		Text:      t.FullText,
		Author:    t.User.ScreenName,
		Permalink: "https://twitter.com/" + t.User.ScreenName + "/status/" + t.IDStr,
		CreatedAt: created,
		Likes:     t.FavoriteCount,
		Retweets:  t.RetweetCount,
		Replies:   t.ReplyCount,
	}
	seen := make(map[string]bool)
	for _, m := range append(append([]rawMedia{}, t.ExtendedEntities.Media...), t.Entities.Media...) {
		if m.MediaURLHTTPS == "" || seen[m.MediaURLHTTPS] {
			continue
		}
		seen[m.MediaURLHTTPS] = true
		tw.Media = append(tw.Media, Media{URL: m.MediaURLHTTPS, Type: m.Type})
	}
	return tw
}
