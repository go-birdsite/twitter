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
c := twitter.New()
tl, err := c.UserTweets(context.Background(), "jack")
for _, tw := range tl.Tweets {
    fmt.Printf("@%s: %s (%d likes)\n", tw.Author, tw.Text, tw.Likes)
}
```

## ⚠️ Fragility & Terms of Service

This is inherently fragile. Twitter/X changes and locks these endpoints, and
many profiles or rate states require a valid auth token (`WithAuthToken`).
Blocked requests (403/429) surface as errors. Respect Twitter/X's Terms of
Service and applicable law when using this library.

## License

BSD-3-Clause © the go-birdsite/twitter authors.
