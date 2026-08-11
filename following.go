package twitter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// DefaultAPIBaseURL is the origin X serves its private GraphQL API from. Unlike
// the public syndication host ([DefaultBaseURL]) this one requires the logged-in
// auth_token + ct0 cookies (see [WithSessionCookies]).
const DefaultAPIBaseURL = "https://x.com"

// DefaultWebBearer is the public web-app bearer token X's own site sends on every
// GraphQL call. It is not a per-user secret — the same constant is used by every
// logged-out and logged-in web session; the real authentication is the
// auth_token + ct0 cookies. X may rotate it, in which case the call is rejected
// and [Client.Following] reports it (see [ErrNeedsAuth]).
const DefaultWebBearer = "AAAAAAAAAAAAAAAAAAAAANRILgAAAAAAnNwIzUejRCOuH5E6I8xnZz4puTs=" +
	"1Zv7ttfk8LF81IUq16cHjhLTvJu4FA33AGWWjCpTnA"

// DefaultFollowingQueryID is the GraphQL query id for the Following operation. X
// rotates these ids; when it does the endpoint 404s and [Client.Following]
// reports [ErrQueryIDRotated] so a caller can say so precisely.
const DefaultFollowingQueryID = "iSicc7LrzWGBgDPL0tM_TQ"

var (
	// ErrNeedsAuth reports that the Following call cannot proceed: the auth_token
	// + ct0 session cookies are missing, or X refused the read as unauthenticated
	// (401/403) — the session expired or the bearer rotated.
	ErrNeedsAuth = errors.New("twitter: the Following list needs a logged-in session (auth_token + ct0)")

	// ErrQueryIDRotated reports that X answered 404: the GraphQL query id is stale
	// (X rotated it). The read cannot succeed until the id is updated.
	ErrQueryIDRotated = errors.New("twitter: the Following GraphQL query id rotated (404)")
)

// FollowedUser is one account the viewer follows.
type FollowedUser struct {
	ID         string // the account's numeric rest id
	ScreenName string // handle, without "@"
	Name       string // display name
}

// FollowingPage is one page of the viewer's Following list. Cursor is the bottom
// pagination cursor to pass on the next call, and is "" when the list is
// exhausted.
type FollowingPage struct {
	Users  []FollowedUser
	Cursor string
}

// followingFeatures is the feature-flag block X requires on the Following call.
// The exact set changes over time; a stale set makes X reject the call, surfaced
// as an error.
var followingFeatures = map[string]any{
	"responsive_web_graphql_timeline_navigation_enabled":                true,
	"responsive_web_graphql_exclude_directive_enabled":                  true,
	"verified_phone_label_enabled":                                      false,
	"creator_subscriptions_tweet_preview_api_enabled":                   true,
	"responsive_web_graphql_skip_user_profile_image_extensions_enabled": false,
	"rweb_tipjar_consumption_enabled":                                   true,
	"highlights_tweets_tab_ui_enabled":                                  true,
	"hidden_profile_likes_enabled":                                      true,
	"hidden_profile_subscriptions_enabled":                              true,
	"subscriptions_verification_info_is_identity_verified_enabled":      true,
	"subscriptions_verification_info_verified_since_enabled":            true,
}

// Following returns one page of the accounts userID follows, starting at cursor
// ("" for the first page). userID is the viewer's own numeric id (read, for
// example, from the twid cookie). It calls X's private GraphQL Following query
// with the web bearer and the auth_token + ct0 cookies, unwraps the timeline's
// user entries, and returns the bottom pagination cursor.
//
// It requires a logged-in session: without the auth_token + ct0 cookies (see
// [WithSessionCookies]) it returns [ErrNeedsAuth] without a request. A 401/403
// (expired session / rotated bearer) also maps to [ErrNeedsAuth], and a 404
// (rotated query id) to [ErrQueryIDRotated], so a caller can report each case
// precisely rather than as a generic failure.
func (c *Client) Following(ctx context.Context, userID, cursor string) (*FollowingPage, error) {
	if c.SessionAuthToken == "" || c.CSRFToken == "" {
		return nil, ErrNeedsAuth
	}

	variables := map[string]any{
		"userId":                 userID,
		"count":                  20,
		"includePromotedContent": false,
	}
	if cursor != "" {
		variables["cursor"] = cursor
	}
	// Both blocks are fixed-shape maps of JSON-safe scalars, so json.Marshal
	// cannot fail; the error is discarded rather than left as an untestable branch.
	vb, _ := json.Marshal(variables)
	fb, _ := json.Marshal(followingFeatures)

	q := url.Values{}
	q.Set("variables", string(vb))
	q.Set("features", string(fb))
	endpoint := c.apiBaseURL() + "/i/api/graphql/" + c.followingQueryID() + "/Following?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("twitter: build request: %w", err)
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Authorization", "Bearer "+c.bearer())
	req.Header.Set("x-csrf-token", c.CSRFToken)
	req.Header.Set("x-twitter-auth-type", "OAuth2Session")
	req.Header.Set("x-twitter-active-user", "yes")
	req.Header.Set("Cookie", "auth_token="+c.SessionAuthToken+"; ct0="+c.CSRFToken)

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
		return nil, followingStatusError(resp.StatusCode, body)
	}

	var parsed followingResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("twitter: decode Following response: %w", err)
	}
	users, next := collectFollowing(parsed)
	return &FollowingPage{Users: users, Cursor: next}, nil
}

// followingStatusError maps a non-2xx Following status to the most precise error:
// 401/403 to [ErrNeedsAuth] and 404 to [ErrQueryIDRotated], everything else to a
// generic status error carrying a body snippet.
func followingStatusError(code int, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrNeedsAuth, code)
	case http.StatusNotFound:
		return fmt.Errorf("%w", ErrQueryIDRotated)
	default:
		return fmt.Errorf("twitter: Following: unexpected status %d: %s", code, followingSnippet(body))
	}
}

// followingSnippet returns a short printable prefix of a response body.
func followingSnippet(b []byte) string {
	const max = 160
	if len(b) > max {
		return string(b[:max]) + "…"
	}
	return string(b)
}

// collectFollowing walks the Following timeline instructions, mapping each user
// entry into a FollowedUser and returning the bottom pagination cursor.
func collectFollowing(r followingResponse) ([]FollowedUser, string) {
	var users []FollowedUser
	var cursor string
	for _, ins := range r.Data.User.Result.Timeline.Timeline.Instructions {
		for _, e := range ins.Entries {
			if e.Content.EntryType == "TimelineTimelineCursor" {
				if e.Content.CursorType == "Bottom" {
					cursor = e.Content.Value
				}
				continue
			}
			u := e.Content.ItemContent.UserResults.Result
			if u.RestID == "" || u.Legacy.ScreenName == "" {
				continue // a non-user entry (who-to-follow header, empty, unavailable)
			}
			users = append(users, FollowedUser{
				ID:         u.RestID,
				ScreenName: u.Legacy.ScreenName,
				Name:       u.Legacy.Name,
			})
		}
	}
	return users, cursor
}

// apiBaseURL returns the configured GraphQL origin or the default.
func (c *Client) apiBaseURL() string {
	if c.APIBaseURL != "" {
		return strings.TrimRight(c.APIBaseURL, "/")
	}
	return DefaultAPIBaseURL
}

// bearer returns the configured web bearer or the default.
func (c *Client) bearer() string {
	if c.Bearer != "" {
		return c.Bearer
	}
	return DefaultWebBearer
}

// followingQueryID returns the configured Following query id or the default.
func (c *Client) followingQueryID() string {
	if c.FollowingQueryID != "" {
		return c.FollowingQueryID
	}
	return DefaultFollowingQueryID
}

// followingResponse mirrors the subset of X's Following GraphQL response consumed.
type followingResponse struct {
	Data struct {
		User struct {
			Result struct {
				Timeline struct {
					Timeline struct {
						Instructions []struct {
							Type    string           `json:"type"`
							Entries []followingEntry `json:"entries"`
						} `json:"instructions"`
					} `json:"timeline"`
				} `json:"timeline"`
			} `json:"result"`
		} `json:"user"`
	} `json:"data"`
}

// followingEntry is one timeline entry — a followed-user item or a cursor.
type followingEntry struct {
	EntryID string `json:"entryId"`
	Content struct {
		EntryType   string `json:"entryType"`
		CursorType  string `json:"cursorType"`
		Value       string `json:"value"`
		ItemContent struct {
			UserResults struct {
				Result struct {
					RestID string `json:"rest_id"`
					Legacy struct {
						ScreenName string `json:"screen_name"`
						Name       string `json:"name"`
					} `json:"legacy"`
				} `json:"result"`
			} `json:"user_results"`
		} `json:"itemContent"`
	} `json:"content"`
}
