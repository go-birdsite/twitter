// Package twitter is a dependency-free, best-effort read client for public
// Twitter/X profile timelines. It uses the public syndication timeline endpoint
// that powers embedded timeline widgets, extracting the tweets from the
// __NEXT_DATA__ JSON blob in the returned HTML.
//
// This is inherently fragile: Twitter/X changes and locks these endpoints, and
// some profiles or rate states require a valid auth token. Requests that are
// blocked surface as errors rather than pretending to be reliable.
//
// # Client fingerprinting
//
// The endpoint answers 429 to Go's stock net/http even when the account's quota
// is untouched, because it fingerprints the TLS/HTTP2 handshake rather than the
// User-Agent. Pass a browser-fingerprinting http.Client via WithHTTPClient (for
// example github.com/go-browserhttp/browserhttp) for anything beyond a one-shot
// read. ErrFingerprinted reports that case so callers can say so precisely
// instead of blaming a missing token.
package twitter

import (
	"context"
	"encoding/json"
	"errors"
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

// maxNestDepth caps how far mapTweet follows retweeted/quoted references, so a
// hostile or looping payload cannot drive unbounded recursion.
const maxNestDepth = 3

var (
	// ErrFingerprinted reports a 429 refusal aimed at the HTTP client's TLS
	// fingerprint rather than at the account quota. Retrying with the same client
	// never succeeds; use a browser-fingerprinting http.Client instead.
	ErrFingerprinted = errors.New("twitter: request refused (429): the endpoint fingerprints the TLS client — use a browser-fingerprinting http.Client")

	// ErrNotFound reports an unknown, suspended or renamed screen name.
	ErrNotFound = errors.New("twitter: no such account")

	// ErrProtected reports an account whose tweets are not public.
	ErrProtected = errors.New("twitter: account is protected")
)

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

	// The fields below configure the private GraphQL API used by
	// [Client.Following]; they do not affect the public syndication reads above.

	// APIBaseURL is the GraphQL origin; defaults to [DefaultAPIBaseURL].
	APIBaseURL string
	// SessionAuthToken is the logged-in "auth_token" cookie the Following call
	// authenticates with.
	SessionAuthToken string
	// CSRFToken is the logged-in "ct0" cookie, sent as the ct0 cookie and the
	// x-csrf-token header on the Following call.
	CSRFToken string
	// Bearer overrides the web-app bearer token; defaults to [DefaultWebBearer].
	Bearer string
	// FollowingQueryID overrides the Following GraphQL query id; defaults to
	// [DefaultFollowingQueryID].
	FollowingQueryID string
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

// WithAPIBaseURL overrides the GraphQL origin used by [Client.Following] (used
// in tests).
func WithAPIBaseURL(u string) Option {
	return func(c *Client) { c.APIBaseURL = strings.TrimRight(u, "/") }
}

// WithSessionCookies sets the logged-in auth_token + ct0 cookies that
// [Client.Following] authenticates with.
func WithSessionCookies(authToken, csrf string) Option {
	return func(c *Client) { c.SessionAuthToken, c.CSRFToken = authToken, csrf }
}

// WithBearer overrides the web-app bearer token sent on the Following call.
func WithBearer(token string) Option { return func(c *Client) { c.Bearer = token } }

// WithFollowingQueryID overrides the Following GraphQL query id.
func WithFollowingQueryID(id string) Option {
	return func(c *Client) { c.FollowingQueryID = id }
}

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

// VideoVariant is one encoding of a video or animated GIF attachment.
type VideoVariant struct {
	URL         string
	ContentType string // "video/mp4", "application/x-mpegURL", …
	Bitrate     int    // bits per second; 0 for playlists
}

// Media is an attachment on a tweet. For a video or animated GIF, URL is the
// still preview image and Variants carries the playable encodings.
type Media struct {
	URL        string // media_url_https: the photo, or a video's preview frame
	Type       string // "photo" | "video" | "animated_gif"
	AltText    string // author-supplied accessibility description ("" if none)
	Width      int    // original pixel width (0 if unknown)
	Height     int    // original pixel height (0 if unknown)
	DurationMS int    // video/GIF duration in milliseconds (0 for photos)
	Variants   []VideoVariant
}

// BestVariant returns the highest-bitrate progressive MP4 encoding, which is the
// one a plain video decoder can play. ok is false for photos, and for videos
// offered only as an HLS playlist.
func (m Media) BestVariant() (v VideoVariant, ok bool) {
	for _, cand := range m.Variants {
		if cand.ContentType != "video/mp4" || cand.URL == "" {
			continue
		}
		if !ok || cand.Bitrate > v.Bitrate {
			v, ok = cand, true
		}
	}
	return v, ok
}

// Link is a URL in a tweet's text: the t.co shortener, plus what it points at.
type Link struct {
	URL      string // the t.co short URL as it appears in Text
	Expanded string // the real destination
	Display  string // the human-readable form Twitter renders ("nasa.gov/live")
}

// User is the author of a tweet.
type User struct {
	ID          string
	ScreenName  string // handle, without "@"
	Name        string // display name
	AvatarURL   string // profile picture (https)
	Description string // bio
	Verified    bool   // legacy verified or Blue-verified
	Followers   int
	Protected   bool
}

// Tweet is a single normalized tweet.
type Tweet struct {
	ID        string
	Text      string
	Author    string // screen name; the same value as User.ScreenName
	User      User
	Permalink string
	CreatedAt time.Time
	Likes     int
	Retweets  int
	Replies   int
	Quotes    int
	Lang      string
	Sensitive bool
	Media     []Media
	Links     []Link
	// Retweeted is the original tweet when this entry is a retweet, else nil. Its
	// own Text is the real content; this tweet's Text is the "RT @x: …" stub.
	Retweeted *Tweet
	// Quoted is the tweet this one quotes, else nil.
	Quoted *Tweet
}

// Original returns the tweet carrying the actual content: the retweeted tweet
// for a retweet, otherwise the tweet itself.
func (t Tweet) Original() Tweet {
	if t.Retweeted != nil {
		return *t.Retweeted
	}
	return t
}

// ExpandedText returns Text with every t.co short URL replaced by its real
// destination, so a reader can display and follow the links it actually shows.
func (t Tweet) ExpandedText() string {
	out := t.Text
	for _, l := range t.Links {
		if l.URL != "" && l.Expanded != "" {
			out = strings.ReplaceAll(out, l.URL, l.Expanded)
		}
	}
	return out
}

// PrimaryLink returns the first external destination the tweet points at, or ""
// when it links nowhere. Links to Twitter/X itself (a quoted tweet's permalink,
// a photo page) are skipped: they are not "an article this post links out to".
func (t Tweet) PrimaryLink() string {
	for _, l := range t.Links {
		if l.Expanded != "" && !isTwitterHost(l.Expanded) {
			return l.Expanded
		}
	}
	return ""
}

// isTwitterHost reports whether raw points at Twitter/X's own web front end.
func isTwitterHost(raw string) bool {
	s := raw
	for _, p := range []string{"https://", "http://"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.TrimPrefix(s, "www.")
	for _, h := range []string{"twitter.com/", "x.com/", "mobile.twitter.com/"} {
		if strings.HasPrefix(s, h) {
			return true
		}
	}
	return false
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
		return nil, statusError(screenName, resp.StatusCode)
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
		tl.Tweets = append(tl.Tweets, mapTweet(e.Content.Tweet, 0))
	}
	// A protected account still renders a page, headed by its profile, with an
	// empty timeline. Report that rather than an empty, healthy-looking result.
	if len(tl.Tweets) == 0 && doc.Props.PageProps.HeaderProps.User.Protected {
		return nil, fmt.Errorf("%w: @%s", ErrProtected, screenName)
	}
	return tl, nil
}

// statusError maps a non-2xx status to the most precise error available, so the
// caller does not have to guess (and does not report "needs a token" for what is
// really a client-fingerprint refusal).
func statusError(screenName string, code int) error {
	switch code {
	case http.StatusTooManyRequests:
		return fmt.Errorf("%w (@%s)", ErrFingerprinted, screenName)
	case http.StatusNotFound:
		return fmt.Errorf("%w: @%s", ErrNotFound, screenName)
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w: @%s", ErrProtected, screenName)
	default:
		return fmt.Errorf("twitter: GET %s: unexpected status %d", screenName, code)
	}
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
			HeaderProps struct {
				User rawUser `json:"user"`
			} `json:"headerProps"`
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
	IDStr         string  `json:"id_str"`
	FullText      string  `json:"full_text"`
	CreatedAt     string  `json:"created_at"`
	FavoriteCount int     `json:"favorite_count"`
	RetweetCount  int     `json:"retweet_count"`
	ReplyCount    int     `json:"reply_count"`
	QuoteCount    int     `json:"quote_count"`
	Lang          string  `json:"lang"`
	Sensitive     bool    `json:"possibly_sensitive"`
	User          rawUser `json:"user"`
	Entities      struct {
		Media []rawMedia `json:"media"`
		URLs  []rawURL   `json:"urls"`
	} `json:"entities"`
	ExtendedEntities struct {
		Media []rawMedia `json:"media"`
	} `json:"extended_entities"`
	RetweetedStatus *rawTweet `json:"retweeted_status"`
	QuotedStatus    *rawTweet `json:"quoted_status"`
	QuotedTweet     *rawTweet `json:"quoted_tweet"`
}

type rawUser struct {
	IDStr           string `json:"id_str"`
	ScreenName      string `json:"screen_name"`
	Name            string `json:"name"`
	ProfileImageURL string `json:"profile_image_url_https"`
	Description     string `json:"description"`
	Verified        bool   `json:"verified"`
	BlueVerified    bool   `json:"is_blue_verified"`
	FollowersCount  int    `json:"followers_count"`
	Protected       bool   `json:"protected"`
}

type rawURL struct {
	URL         string `json:"url"`
	ExpandedURL string `json:"expanded_url"`
	DisplayURL  string `json:"display_url"`
}

type rawMedia struct {
	MediaURLHTTPS string `json:"media_url_https"`
	Type          string `json:"type"`
	AltText       string `json:"ext_alt_text"`
	OriginalInfo  struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"original_info"`
	VideoInfo struct {
		DurationMillis int `json:"duration_millis"`
		Variants       []struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
			Bitrate     int    `json:"bitrate"`
		} `json:"variants"`
	} `json:"video_info"`
}

func mapUser(u rawUser) User {
	return User{
		ID:          u.IDStr,
		ScreenName:  u.ScreenName,
		Name:        u.Name,
		AvatarURL:   u.ProfileImageURL,
		Description: u.Description,
		Verified:    u.Verified || u.BlueVerified,
		Followers:   u.FollowersCount,
		Protected:   u.Protected,
	}
}

// mapTweet normalizes one raw tweet. depth bounds how far nested
// retweeted/quoted references are followed.
func mapTweet(t rawTweet, depth int) Tweet {
	created, _ := time.Parse(twitterTimeLayout, t.CreatedAt) // zero time on failure
	tw := Tweet{
		ID:        t.IDStr,
		Text:      t.FullText,
		Author:    t.User.ScreenName,
		User:      mapUser(t.User),
		Permalink: "https://twitter.com/" + t.User.ScreenName + "/status/" + t.IDStr,
		CreatedAt: created,
		Likes:     t.FavoriteCount,
		Retweets:  t.RetweetCount,
		Replies:   t.ReplyCount,
		Quotes:    t.QuoteCount,
		Lang:      t.Lang,
		Sensitive: t.Sensitive,
	}
	for _, u := range t.Entities.URLs {
		if u.URL == "" {
			continue
		}
		tw.Links = append(tw.Links, Link{URL: u.URL, Expanded: u.ExpandedURL, Display: u.DisplayURL})
	}
	// extended_entities carries the full media set (all four photos, video info);
	// entities.media only ever repeats the first one, so it is the fallback.
	seen := make(map[string]bool)
	for _, m := range append(append([]rawMedia{}, t.ExtendedEntities.Media...), t.Entities.Media...) {
		if m.MediaURLHTTPS == "" || seen[m.MediaURLHTTPS] {
			continue
		}
		seen[m.MediaURLHTTPS] = true
		tw.Media = append(tw.Media, mapMedia(m))
	}
	if depth < maxNestDepth {
		if t.RetweetedStatus != nil {
			inner := mapTweet(*t.RetweetedStatus, depth+1)
			tw.Retweeted = &inner
		}
		if q := t.QuotedStatus; q != nil {
			inner := mapTweet(*q, depth+1)
			tw.Quoted = &inner
		} else if q := t.QuotedTweet; q != nil {
			inner := mapTweet(*q, depth+1)
			tw.Quoted = &inner
		}
	}
	return tw
}

func mapMedia(m rawMedia) Media {
	out := Media{
		URL:        m.MediaURLHTTPS,
		Type:       m.Type,
		AltText:    m.AltText,
		Width:      m.OriginalInfo.Width,
		Height:     m.OriginalInfo.Height,
		DurationMS: m.VideoInfo.DurationMillis,
	}
	for _, v := range m.VideoInfo.Variants {
		out.Variants = append(out.Variants, VideoVariant{URL: v.URL, ContentType: v.ContentType, Bitrate: v.Bitrate})
	}
	return out
}
