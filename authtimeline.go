package twitter

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// DefaultUserByScreenNameQueryID is the GraphQL query id for the UserByScreenName
// operation, which resolves a handle to its numeric rest id. X rotates these ids;
// when it does the endpoint 404s and the call reports [ErrQueryIDRotated].
const DefaultUserByScreenNameQueryID = "Gb-d6r0vxPOADdG62OEBpQ"

// DefaultUserTweetsQueryID is the GraphQL query id for the UserTweets operation,
// which returns an account's own timeline. X rotates these ids; a 404 surfaces as
// [ErrQueryIDRotated].
const DefaultUserTweetsQueryID = "SXVCYB8XHSS25nzIljNtZA"

// userByScreenNameFeatures is the feature-flag block the UserByScreenName query
// requires. A stale set makes X reject the call (400), surfaced as a status error.
var userByScreenNameFeatures = map[string]any{
	"hidden_profile_subscriptions_enabled":                              true,
	"profile_label_improvements_pcf_label_in_post_enabled":              true,
	"rweb_tipjar_consumption_enabled":                                   true,
	"responsive_web_graphql_exclude_directive_enabled":                  true,
	"verified_phone_label_enabled":                                      false,
	"subscriptions_verification_info_is_identity_verified_enabled":      true,
	"subscriptions_verification_info_verified_since_enabled":            true,
	"highlights_tweets_tab_ui_enabled":                                  true,
	"responsive_web_twitter_article_notes_tab_enabled":                  true,
	"subscriptions_feature_can_gift_premium":                            true,
	"creator_subscriptions_tweet_preview_api_enabled":                   true,
	"responsive_web_graphql_skip_user_profile_image_extensions_enabled": false,
	"responsive_web_graphql_timeline_navigation_enabled":                true,
}

// userTweetsFeatures is the feature-flag block the UserTweets query requires.
var userTweetsFeatures = map[string]any{
	"rweb_video_screen_enabled":                                               false,
	"profile_label_improvements_pcf_label_in_post_enabled":                    true,
	"rweb_tipjar_consumption_enabled":                                         true,
	"responsive_web_graphql_exclude_directive_enabled":                        true,
	"verified_phone_label_enabled":                                            false,
	"creator_subscriptions_tweet_preview_api_enabled":                         true,
	"responsive_web_graphql_timeline_navigation_enabled":                      true,
	"responsive_web_graphql_skip_user_profile_image_extensions_enabled":       false,
	"premium_content_api_read_enabled":                                        false,
	"communities_web_enable_tweet_community_results_fetch":                    true,
	"c9s_tweet_anatomy_moderator_badge_enabled":                               true,
	"responsive_web_grok_analyze_button_fetch_trends_enabled":                 false,
	"responsive_web_grok_analyze_post_followups_enabled":                      true,
	"responsive_web_jetfuel_frame":                                            false,
	"responsive_web_grok_share_attachment_enabled":                            true,
	"articles_preview_enabled":                                                true,
	"responsive_web_edit_tweet_api_enabled":                                   true,
	"graphql_is_translatable_rweb_tweet_is_translatable_enabled":              true,
	"view_counts_everywhere_api_enabled":                                      true,
	"longform_notetweets_consumption_enabled":                                 true,
	"responsive_web_twitter_article_tweet_consumption_enabled":                true,
	"tweet_awards_web_tipping_enabled":                                        false,
	"responsive_web_grok_show_grok_translated_post":                           false,
	"responsive_web_grok_analysis_button_from_backend":                        true,
	"creator_subscriptions_quote_tweet_preview_enabled":                       false,
	"freedom_of_speech_not_reach_fetch_enabled":                               true,
	"standardized_nudges_misinfo":                                             true,
	"tweet_with_visibility_results_prefer_gql_limited_actions_policy_enabled": true,
	"longform_notetweets_rich_text_read_enabled":                              true,
	"longform_notetweets_inline_media_enabled":                                true,
	"responsive_web_grok_image_annotation_enabled":                            true,
	"responsive_web_enhance_cards_enabled":                                    false,
}

// UserTweetsAuth returns a public account's recent tweets through the private
// GraphQL API, authenticated with the logged-in auth_token + ct0 cookies. Unlike
// [Client.UserTweets] — which reads the public syndication endpoint that X
// rate-limits (429) to a request or two per client — the authenticated path is
// served under the session's own, far higher quota, so it can back a reader that
// follows many accounts.
//
// It makes two calls: UserByScreenName to resolve the handle to its numeric id,
// then UserTweets for the timeline. A caller that already knows the id (they are
// permanent) can skip the first with [Client.UserTweetsByID].
//
// It requires a logged-in session: without the auth_token + ct0 cookies (see
// [WithSessionCookies]) it returns [ErrNeedsAuth] without a request. A 401/403
// (expired session / rotated bearer) maps to [ErrNeedsAuth], a 404 (rotated query
// id) to [ErrQueryIDRotated], and an unknown handle to [ErrNotFound].
func (c *Client) UserTweetsAuth(ctx context.Context, screenName string) (*Timeline, error) {
	id, err := c.UserIDByScreenName(ctx, screenName)
	if err != nil {
		return nil, err
	}
	return c.UserTweetsByID(ctx, id)
}

// UserIDByScreenName resolves a handle (without the leading "@") to its numeric
// rest id via the authenticated UserByScreenName GraphQL query. The id is
// permanent, so a caller may cache it. It requires session cookies (else
// [ErrNeedsAuth]); an unknown, suspended or renamed handle maps to [ErrNotFound].
func (c *Client) UserIDByScreenName(ctx context.Context, screenName string) (string, error) {
	if c.SessionAuthToken == "" || c.CSRFToken == "" {
		return "", ErrNeedsAuth
	}
	vars := map[string]any{"screen_name": screenName}
	body, err := c.authGraphGet(ctx, c.userByScreenNameQueryID(), "UserByScreenName", vars, userByScreenNameFeatures)
	if err != nil {
		return "", err
	}
	var r struct {
		Data struct {
			User struct {
				Result struct {
					RestID string `json:"rest_id"`
				} `json:"result"`
			} `json:"user"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil {
		return "", fmt.Errorf("twitter: decode UserByScreenName response: %w", err)
	}
	if r.Data.User.Result.RestID == "" {
		return "", fmt.Errorf("%w: @%s", ErrNotFound, screenName)
	}
	return r.Data.User.Result.RestID, nil
}

// UserTweetsByID fetches an account's timeline via the authenticated UserTweets
// GraphQL query, given its numeric rest id (see [Client.UserIDByScreenName]). It
// requires session cookies (else [ErrNeedsAuth]).
func (c *Client) UserTweetsByID(ctx context.Context, userID string) (*Timeline, error) {
	if c.SessionAuthToken == "" || c.CSRFToken == "" {
		return nil, ErrNeedsAuth
	}
	vars := map[string]any{
		"userId":                                 userID,
		"count":                                  20,
		"includePromotedContent":                 false,
		"withQuickPromoteEligibilityTweetFields": false,
		"withVoice":                              false,
		"withV2Timeline":                         true,
	}
	body, err := c.authGraphGet(ctx, c.userTweetsQueryID(), "UserTweets", vars, userTweetsFeatures)
	if err != nil {
		return nil, err
	}
	var r userTweetsResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("twitter: decode UserTweets response: %w", err)
	}
	return &Timeline{Tweets: collectUserTweets(r)}, nil
}

// authGraphGet performs an authenticated GraphQL GET — the web bearer plus the
// logged-in auth_token + ct0 cookies X's own site sends — and returns the body,
// mapping a non-2xx status to the most precise error (401/403 → [ErrNeedsAuth],
// 404 → [ErrQueryIDRotated]).
func (c *Client) authGraphGet(ctx context.Context, queryID, op string, vars, feats map[string]any) ([]byte, error) {
	// Both blocks are fixed-shape maps of JSON-safe scalars, so json.Marshal
	// cannot fail; the error is discarded rather than left as an untestable branch.
	vb, _ := json.Marshal(vars)
	fb, _ := json.Marshal(feats)
	q := url.Values{}
	q.Set("variables", string(vb))
	q.Set("features", string(fb))
	endpoint := c.apiBaseURL() + "/i/api/graphql/" + queryID + "/" + op + "?" + q.Encode()

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
		return nil, graphStatusError(op, resp.StatusCode, body)
	}
	return body, nil
}

// graphStatusError maps a non-2xx authenticated-GraphQL status to the most precise
// error: 401/403 to [ErrNeedsAuth] and 404 to [ErrQueryIDRotated], everything else
// to a generic status error carrying a body snippet (a stale features set shows up
// here as a 400 with X's own explanation).
func graphStatusError(op string, code int, body []byte) error {
	switch code {
	case http.StatusUnauthorized, http.StatusForbidden:
		return fmt.Errorf("%w (status %d)", ErrNeedsAuth, code)
	case http.StatusNotFound:
		return fmt.Errorf("%w", ErrQueryIDRotated)
	default:
		return fmt.Errorf("twitter: %s: unexpected status %d: %s", op, code, followingSnippet(body))
	}
}

// userByScreenNameQueryID returns the configured id or the default.
func (c *Client) userByScreenNameQueryID() string {
	if c.UserByScreenNameQueryID != "" {
		return c.UserByScreenNameQueryID
	}
	return DefaultUserByScreenNameQueryID
}

// userTweetsQueryID returns the configured id or the default.
func (c *Client) userTweetsQueryID() string {
	if c.UserTweetsQueryID != "" {
		return c.UserTweetsQueryID
	}
	return DefaultUserTweetsQueryID
}

// collectUserTweets walks the UserTweets timeline instructions, mapping each tweet
// entry (and the pinned entry, and each tweet inside a self-thread module) into a
// normalized Tweet. Cursor and non-tweet entries are skipped.
func collectUserTweets(r userTweetsResponse) []Tweet {
	var tweets []Tweet
	add := func(g *graphTweetResult) {
		if tw, ok := mapGraphTweet(g, 0); ok {
			tweets = append(tweets, tw)
		}
	}
	for _, ins := range r.Data.User.Result.Timeline.Timeline.Instructions {
		if ins.Entry != nil { // TimelinePinEntry carries a single pinned tweet
			add(&ins.Entry.Content.ItemContent.TweetResults.Result)
		}
		for _, e := range ins.Entries {
			add(&e.Content.ItemContent.TweetResults.Result)
			for _, it := range e.Content.Items { // a self-thread module
				add(&it.Item.ItemContent.TweetResults.Result)
			}
		}
	}
	return tweets
}

// mapGraphTweet normalizes one GraphQL tweet_results.result into a Tweet, reusing
// the syndication tweet mapper for the shared "legacy" fields and grafting on the
// author (which GraphQL carries separately, under core.user_results) and the
// nested retweeted / quoted tweets (carried as *_result wrappers rather than the
// inline retweeted_status the syndication payload uses). ok is false for a cursor,
// an unavailable tweet, or any entry that carries no tweet.
func mapGraphTweet(g *graphTweetResult, depth int) (Tweet, bool) {
	if g == nil {
		return Tweet{}, false
	}
	if g.Tweet != nil { // TweetWithVisibilityResults wraps the real tweet
		g = g.Tweet
	}
	lt := g.Legacy.rawTweet
	if lt.IDStr == "" {
		lt.IDStr = g.RestID
	}
	lt.User = g.Core.UserResults.Result.Legacy
	if lt.User.IDStr == "" {
		lt.User.IDStr = g.Core.UserResults.Result.RestID
	}
	if lt.IDStr == "" {
		return Tweet{}, false // a cursor or empty/unavailable entry
	}
	// maxNestDepth here stops mapTweet from following its own (always-nil for a
	// GraphQL payload) inline retweeted_status: the nesting is grafted on below
	// from the GraphQL *_result wrappers instead.
	tw := mapTweet(lt, maxNestDepth)
	if depth < maxNestDepth {
		if inner, ok := mapGraphTweet(g.Legacy.RetweetedStatusResult.Result, depth+1); ok {
			tw.Retweeted = &inner
		}
		if inner, ok := mapGraphTweet(g.Legacy.QuotedStatusResult.Result, depth+1); ok {
			tw.Quoted = &inner
		}
	}
	return tw, true
}

// userTweetsResponse mirrors the subset of X's UserTweets GraphQL response used.
type userTweetsResponse struct {
	Data struct {
		User struct {
			Result struct {
				// The profile timeline nests as result.timeline.timeline.instructions
				// (X keeps the doubled key even when withV2Timeline is requested).
				Timeline struct {
					Timeline struct {
						Instructions []userTimelineInstruction `json:"instructions"`
					} `json:"timeline"`
				} `json:"timeline"`
			} `json:"result"`
		} `json:"user"`
	} `json:"data"`
}

// userTimelineInstruction is one timeline instruction: TimelineAddEntries carries
// Entries, TimelinePinEntry a single Entry.
type userTimelineInstruction struct {
	Type    string              `json:"type"`
	Entries []userTimelineEntry `json:"entries"`
	Entry   *userTimelineEntry  `json:"entry"`
}

// userTimelineEntry is one timeline entry: a single tweet, a self-thread module of
// tweets, or a cursor.
type userTimelineEntry struct {
	EntryID string `json:"entryId"`
	Content struct {
		EntryType   string `json:"entryType"`
		ItemContent struct {
			TweetResults struct {
				Result graphTweetResult `json:"result"`
			} `json:"tweet_results"`
		} `json:"itemContent"`
		Items []struct {
			Item struct {
				ItemContent struct {
					TweetResults struct {
						Result graphTweetResult `json:"result"`
					} `json:"tweet_results"`
				} `json:"itemContent"`
			} `json:"item"`
		} `json:"items"`
	} `json:"content"`
}

// graphTweetResult is one tweet as the GraphQL API returns it: a "Tweet", or a
// "TweetWithVisibilityResults" that wraps the real one under Tweet.
type graphTweetResult struct {
	RestID string `json:"rest_id"`
	Core   struct {
		UserResults struct {
			Result struct {
				RestID string  `json:"rest_id"`
				Legacy rawUser `json:"legacy"`
			} `json:"result"`
		} `json:"user_results"`
	} `json:"core"`
	Legacy graphTweetLegacy  `json:"legacy"`
	Tweet  *graphTweetResult `json:"tweet"`
}

// graphTweetLegacy is a tweet's "legacy" block. It shares its scalar, entity and
// media fields with the syndication payload (embedded [rawTweet]), and adds the
// GraphQL-only nested-tweet wrappers.
type graphTweetLegacy struct {
	rawTweet
	RetweetedStatusResult graphResultWrap `json:"retweeted_status_result"`
	QuotedStatusResult    graphResultWrap `json:"quoted_status_result"`
}

// graphResultWrap wraps a nested tweet result (a retweet's or a quote's original).
type graphResultWrap struct {
	Result *graphTweetResult `json:"result"`
}
