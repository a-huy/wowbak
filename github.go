package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const userAgent = "wowbak (https://github.com/, addon update checker)"

// ghRelease is the part of a GitHub release we care about.
type ghRelease struct {
	TagName    string    `json:"tag_name"`
	Name       string    `json:"name"`
	Draft      bool      `json:"draft"`
	Prerelease bool      `json:"prerelease"`
	Published  time.Time `json:"published_at"`
	HTMLURL    string    `json:"html_url"`
	Assets     []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"browser_download_url"`
}

func (r ghRelease) asset(name string) (ghAsset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return ghAsset{}, false
}

// releaseManifest is the release.json produced by the standard addon packager.
// It says which zip belongs to which game flavor, which is the only reliable way
// to avoid installing a Classic build over a Retail one.
type releaseManifest struct {
	Releases []struct {
		Filename string `json:"filename"`
		NoLib    bool   `json:"nolib"`
		Metadata []struct {
			Flavor    string `json:"flavor"`
			Interface int    `json:"interface"`
		} `json:"metadata"`
	} `json:"releases"`
}

type ghClient struct {
	token string
	http  *http.Client
	// dl has no overall deadline: a large addon on a slow connection is not a
	// failure. Hung connections are still caught by the dial and header timeouts.
	dl *http.Client
	// remaining tracks the rate limit so we can fail with a useful message
	// rather than a bare 403 halfway through a run.
	remaining int
	resetAt   time.Time
}

func newGHClient(token string) *ghClient {
	return &ghClient{
		token: token,
		http:  &http.Client{Timeout: 30 * time.Second},
		dl: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
				TLSHandshakeTimeout:   15 * time.Second,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       60 * time.Second,
			},
		},
		remaining: -1,
	}
}

func (g *ghClient) get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}

	resp, err := g.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if v := resp.Header.Get("X-RateLimit-Remaining"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			g.remaining = n
		}
	}
	if v := resp.Header.Get("X-RateLimit-Reset"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			g.resetAt = time.Unix(n, 0)
		}
	}

	// Metadata only; asset downloads go through downloadTo, which does not cap.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, errNotFound
	case resp.StatusCode == http.StatusForbidden && g.remaining == 0:
		wait := time.Until(g.resetAt).Round(time.Minute)
		hint := "set a GitHub token to raise the limit to 5000/hour: wowbak token set <token>"
		if g.token != "" {
			hint = "wait for the limit to reset"
		}
		return nil, fmt.Errorf("GitHub rate limit reached (resets in %v) - %s", wait, hint)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub returned %s for %s", resp.Status, url)
	}
	return body, nil
}

var errNotFound = fmt.Errorf("not found")

// releases lists a repository's releases, newest first.
func (g *ghClient) releases(repo string) ([]ghRelease, error) {
	body, err := g.get("https://api.github.com/repos/" + repo + "/releases?per_page=20")
	if err != nil {
		return nil, err
	}
	var out []ghRelease
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unreadable release list for %s: %w", repo, err)
	}
	return out, nil
}

func (g *ghClient) manifest(a ghAsset) (*releaseManifest, error) {
	body, err := g.get(a.URL)
	if err != nil {
		return nil, err
	}
	var m releaseManifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// downloadTo streams a release asset to a file. Addons run to tens of megabytes
// (Narcissus is over 90MB), so this must not buffer in memory, and must not cap
// the response the way the JSON helper does - a truncated download looks exactly
// like a corrupt archive.
func (g *ghClient) downloadTo(a ghAsset, dest string, progress func(done, total int64)) error {
	req, err := http.NewRequest("GET", a.URL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/octet-stream")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.dl.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GitHub returned %s downloading %s", resp.Status, a.Name)
	}

	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	var done int64
	buf := make([]byte, 256<<10)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return werr
			}
			done += int64(n)
			if progress != nil {
				progress(done, a.Size)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}

	// A short read is the failure that produced "not a valid zip file", so say
	// what actually happened instead of letting the zip reader guess.
	if a.Size > 0 && done != a.Size {
		return fmt.Errorf("downloaded %d of %d bytes - the transfer was cut short", done, a.Size)
	}
	return f.Sync()
}

// rateNote describes remaining budget for the end of a run.
func (g *ghClient) rateNote() string {
	if g.remaining < 0 {
		return ""
	}
	if g.token == "" && g.remaining < 20 {
		return fmt.Sprintf("%d GitHub requests left this hour; a token raises the limit to 5000 "+
			"(wowbak token set <token>)", g.remaining)
	}
	return fmt.Sprintf("%d GitHub requests left this hour", g.remaining)
}

// interfaceMajor turns a .toc Interface number into its expansion number:
// 110105 -> 11 (retail), 50500 -> 5 (Mists), 11507 -> 1 (Classic Era).
func interfaceMajor(iface int) int { return iface / 10000 }

// pickAsset chooses the zip built for the game version an addon is installed
// under. It matches on the interface number in release.json rather than on
// flavor names, which vary between packagers and change with each expansion.
//
// Returns the asset, the flavor it was matched on, and whether a match was found.
func pickAsset(rel ghRelease, m *releaseManifest, wantIface int) (ghAsset, string, bool) {
	if m == nil {
		// No manifest: only safe when the release has exactly one zip, and even
		// then we cannot confirm the flavor, so the caller decides what to do.
		var zips []ghAsset
		for _, a := range rel.Assets {
			if strings.HasSuffix(strings.ToLower(a.Name), ".zip") {
				zips = append(zips, a)
			}
		}
		if len(zips) == 1 {
			return zips[0], "", true
		}
		return ghAsset{}, "", false
	}

	want := interfaceMajor(wantIface)
	var exact, sameMajor ghAsset
	var exactFlavor, majorFlavor string

	for _, r := range m.Releases {
		if r.NoLib {
			continue // the no-libraries build needs its dependencies installed separately
		}
		for _, md := range r.Metadata {
			if md.Interface == wantIface && exactFlavor == "" {
				if a, ok := rel.asset(r.Filename); ok {
					exact, exactFlavor = a, md.Flavor
				}
			}
			if interfaceMajor(md.Interface) == want && majorFlavor == "" {
				if a, ok := rel.asset(r.Filename); ok {
					sameMajor, majorFlavor = a, md.Flavor
				}
			}
		}
	}
	if exactFlavor != "" {
		return exact, exactFlavor, true
	}
	if majorFlavor != "" {
		return sameMajor, majorFlavor, true
	}
	return ghAsset{}, "", false
}
