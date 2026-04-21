package lastfm

import (
	"crypto/md5"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
)

const (
	apiURL  = "https://ws.audioscrobbler.com/2.0/"
	authURL = "https://www.last.fm/api/auth/"
)

// Client talks to the Last.fm API.
type Client struct {
	apiKey    string
	apiSecret string
	http      *http.Client
}

func NewClient(apiKey, apiSecret string) *Client {
	return &Client{
		apiKey:    apiKey,
		apiSecret: apiSecret,
		http:      &http.Client{},
	}
}

// AuthURL returns the URL to redirect users to for Last.fm authorization.
func (c *Client) AuthURL(callbackURL string) string {
	v := url.Values{}
	v.Set("api_key", c.apiKey)
	v.Set("cb", callbackURL)
	return authURL + "?" + v.Encode()
}

// GetSession exchanges an auth token for a permanent session key.
func (c *Client) GetSession(token string) (sessionKey, username string, err error) {
	params := map[string]string{
		"method": "auth.getSession",
		"token":  token,
	}
	body, err := c.post(params)
	if err != nil {
		return "", "", fmt.Errorf("auth.getSession: %w", err)
	}

	var resp struct {
		XMLName xml.Name `xml:"lfm"`
		Status  string   `xml:"status,attr"`
		Session struct {
			Name string `xml:"name"`
			Key  string `xml:"key"`
		} `xml:"session"`
		Error struct {
			Code    int    `xml:"code,attr"`
			Message string `xml:",chardata"`
		} `xml:"error"`
	}
	if err := xml.Unmarshal(body, &resp); err != nil {
		return "", "", fmt.Errorf("auth.getSession: parse: %w", err)
	}
	if resp.Status != "ok" {
		return "", "", fmt.Errorf("auth.getSession: %s (code %d)", resp.Error.Message, resp.Error.Code)
	}
	return resp.Session.Key, resp.Session.Name, nil
}

// UpdateNowPlaying notifies Last.fm that a track is now playing.
func (c *Client) UpdateNowPlaying(sk, artist, track, album string, duration int) error {
	params := map[string]string{
		"method":   "track.updateNowPlaying",
		"sk":       sk,
		"artist":   artist,
		"track":    track,
		"album":    album,
		"duration": fmt.Sprintf("%d", duration),
	}
	_, err := c.post(params)
	if err != nil {
		return fmt.Errorf("track.updateNowPlaying: %w", err)
	}
	return nil
}

// Scrobble records a completed track play.
func (c *Client) Scrobble(sk, artist, track, album string, timestamp int64, duration int) error {
	params := map[string]string{
		"method":       "track.scrobble",
		"sk":           sk,
		"artist[0]":    artist,
		"track[0]":     track,
		"album[0]":     album,
		"timestamp[0]": fmt.Sprintf("%d", timestamp),
		"duration[0]":  fmt.Sprintf("%d", duration),
	}
	_, err := c.post(params)
	if err != nil {
		return fmt.Errorf("track.scrobble: %w", err)
	}
	return nil
}

// sign computes the Last.fm API signature: MD5 of sorted key+value pairs + secret.
func (c *Client) sign(params map[string]string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k == "format" {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	for _, k := range keys {
		buf.WriteString(k)
		buf.WriteString(params[k])
	}
	buf.WriteString(c.apiSecret)

	return fmt.Sprintf("%x", md5.Sum([]byte(buf.String())))
}

// post sends a signed POST request to the Last.fm API.
func (c *Client) post(params map[string]string) ([]byte, error) {
	params["api_key"] = c.apiKey
	params["api_sig"] = c.sign(params)

	form := url.Values{}
	for k, v := range params {
		form.Set(k, v)
	}

	resp, err := c.http.PostForm(apiURL, form)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}
