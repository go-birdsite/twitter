<p align="center"><img src="https://raw.githubusercontent.com/go-birdsite/brand/main/social/go-birdsite.png" alt="go-birdsite/twitter" width="720"></p>

# go-birdsite / twitter

[![CI](https://github.com/go-birdsite/twitter/actions/workflows/ci.yml/badge.svg)](https://github.com/go-birdsite/twitter/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-birdsite/twitter.svg)](https://pkg.go.dev/github.com/go-birdsite/twitter)
[![License: BSD-3-Clause](https://img.shields.io/badge/License-BSD--3--Clause-blue.svg)](LICENSE)

A pure-Go (**CGO=0**), dependency-free, **best-effort** read client for public
Twitter/X profile timelines. It reads the public syndication timeline endpoint
that powers embedded timeline widgets and extracts tweets from the
`__NEXT_DATA__` JSON blob.

```go
c := twitter.New(twitter.WithHTTPClient(browserhttp.NewClient(30 * time.Second)))
tl, err := c.UserTweets(context.Background(), "jack")
for _, tw := range tl.Tweets {
    o := tw.Original() // the retweeted tweet for a retweet, else tw itself
    fmt.Printf("@%s (%s): %s (%d likes)\n", o.User.ScreenName, o.User.Name, o.ExpandedText(), o.Likes)
    for _, m := range o.Media {
        if v, ok := m.BestVariant(); ok {
            fmt.Printf("  video %dx%d: %s\n", m.Width, m.Height, v.URL)
        }
    }
}
```

## 🔑 Use a browser-fingerprinting HTTP client

The endpoint answers **429 to Go's stock `net/http` even when the account's
quota is untouched** — it fingerprints the TLS/HTTP2 handshake, not the
User-Agent. `curl` gets a 200 from the same IP with the same User-Agent in the
same second. Pass a fingerprinting client such as
[go-browserhttp](https://github.com/go-browserhttp/browserhttp) via
`WithHTTPClient` for anything beyond a one-shot read. That refusal is reported
as `ErrFingerprinted`, distinct from `ErrNotFound` (unknown account) and
`ErrProtected` (non-public account), so callers can say what actually happened
instead of blaming a missing token.

## What a tweet carries

Author (handle, display name, avatar, bio, verified, followers) · full text ·
`ExpandedText()` with every `t.co` resolved · `Links` (short/expanded/display) ·
`PrimaryLink()`, the first destination that is not Twitter/X itself · photos and
videos with alt text, pixel size, duration and every encoding, plus
`BestVariant()` for the highest-bitrate progressive MP4 · retweets (`Retweeted`,
`Original()`) and quotes (`Quoted`) · like/retweet/reply/quote counts · language
and sensitivity flags.

## ⚠️ Fragility & Terms of Service

This is inherently fragile. Twitter/X changes and locks these endpoints, and
some profiles or rate states require a valid auth token (`WithAuthToken`).
Blocked requests surface as errors. Respect Twitter/X's Terms of Service and
applicable law when using this library.

## License

BSD-3-Clause © the go-birdsite/twitter authors.
