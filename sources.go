package main

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// A source says where a package's updates come from. Only GitHub is fetched
// from: it is free, documented, and permits automated downloads.
type source struct {
	Kind string // "github"
	Repo string // owner/name
}

func (s source) String() string { return s.Kind + ":" + s.Repo }

func parseSource(v string) (source, bool) {
	kind, rest, ok := strings.Cut(strings.TrimSpace(v), ":")
	if !ok || strings.ToLower(kind) != "github" {
		return source{}, false
	}
	rest = strings.TrimSpace(strings.TrimSuffix(rest, "/"))
	if strings.Count(rest, "/") != 1 || rest == "" {
		return source{}, false
	}
	return source{Kind: "github", Repo: rest}, true
}

// sourceFor returns the configured source for a package, set in wowbak.conf as
//
//	addon.WeakAuras = github:WeakAuras/WeakAuras2
func (c Config) sourceFor(pkg string) (source, bool) {
	if v, ok := c.Addons[strings.ToLower(pkg)]; ok {
		return parseSource(v)
	}
	return source{}, false
}

// ---------------------------------------------------------------- discovery

var (
	reWagoReleases = regexp.MustCompile(`"recent_releases"\s*:\s*`)
	reGitHubRepo   = regexp.MustCompile(`github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:/|"|\s|\)|$)`)
	reWagoSlug     = regexp.MustCompile(`"slug"\s*:\s*"([^"]+)"`)
)

// wagoPage is what a public Wago addon page tells us. The page is read once per
// addon, only to learn where the project actually lives; all version checking
// and downloading then happens against GitHub's documented API.
type wagoPage struct {
	Slug     string
	GitHub   string            // owner/repo, if the page mentions one
	Versions map[string]string // wago flavor -> version label
}

func fetchWagoPage(hc *http.Client, slug string) (*wagoPage, error) {
	req, err := http.NewRequest("GET", "https://addons.wago.io/addons/"+slug, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("wago returned %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	text := html.UnescapeString(string(body))
	p := &wagoPage{Slug: slug, Versions: map[string]string{}}

	if m := reWagoSlug.FindStringSubmatch(text); m != nil {
		p.Slug = m[1]
	}
	if loc := reWagoReleases.FindStringIndex(text); loc != nil {
		if blob := balancedObject(text, loc[1]); blob != "" {
			var raw map[string]struct {
				Label string `json:"label"`
			}
			if json.Unmarshal([]byte(blob), &raw) == nil {
				for flavor, v := range raw {
					if v.Label != "" {
						p.Versions[flavor] = v.Label
					}
				}
			}
		}
	}
	// Project pages link their repository from the changelog and sidebar.
	flat := strings.ReplaceAll(text, `\/`, "/")
	counts := map[string]int{}
	for _, m := range reGitHubRepo.FindAllStringSubmatch(flat, -1) {
		repo := strings.TrimSuffix(m[1], ".git")
		if strings.HasPrefix(repo, "users/") || strings.HasPrefix(repo, "sponsors/") {
			continue
		}
		counts[repo]++
	}
	best, bestN := "", 0
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic when counts tie
	for _, k := range keys {
		if counts[k] > bestN {
			best, bestN = k, counts[k]
		}
	}
	p.GitHub = best
	return p, nil
}

// balancedObject returns the JSON object starting at the first '{' at or after i.
func balancedObject(s string, i int) string {
	start := strings.IndexByte(s[i:], '{')
	if start < 0 {
		return ""
	}
	start += i
	depth, inStr, esc := 0, false, false
	for j := start; j < len(s); j++ {
		c := s[j]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : j+1]
			}
		}
	}
	return ""
}

// slugCandidates guesses the Wago URL slug for a package, since .toc files carry
// Wago's internal id rather than the slug used in links.
func slugCandidates(p Package) []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		s = strings.Map(func(r rune) rune {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				return r
			case r == ' ', r == '_', r == '-':
				return '-'
			}
			return -1
		}, s)
		s = strings.Trim(strings.ReplaceAll(s, "--", "-"), "-")
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(p.Name)
	add(strings.ReplaceAll(p.Name, "_", ""))
	if tok := leadingName(p.Name); tok != "" {
		add(tok)
	}
	return out
}

func leadingName(s string) string {
	if i := strings.IndexAny(s, "-_"); i > 0 {
		return s[:i]
	}
	return ""
}

// discover finds a GitHub repo for a package by reading its public Wago page.
func discover(hc *http.Client, p Package) (source, string, bool) {
	for _, slug := range slugCandidates(p) {
		page, err := fetchWagoPage(hc, slug)
		if err != nil {
			continue
		}
		if page.GitHub != "" {
			return source{Kind: "github", Repo: page.GitHub}, page.Slug, true
		}
	}
	return source{}, "", false
}

func newHTTPClient() *http.Client { return &http.Client{Timeout: 30 * time.Second} }

// cmdSources lists where each package's updates come from, and can fill in the
// gaps by looking each unmatched package up on its public Wago page.
func cmdSources(args sourcesArgs) int {
	cfg := loadConfig()
	install := resolveInstall(args.installPath)
	flavors := resolveFlavors(install, args.flavor)
	hc := newHTTPClient()

	for _, flavor := range flavors {
		pkgs := scanPackages(install, flavor)
		if len(pkgs) == 0 {
			continue
		}
		fmt.Printf("%s\n\n", flavor)

		found, missing := 0, 0
		var discovered [][2]string
		for _, p := range pkgs {
			if src, ok := cfg.sourceFor(p.Name); ok {
				found++
				if args.all {
					fmt.Printf("  %-30s %s\n", p.Name, src)
				}
				continue
			}
			if !args.discover {
				missing++
				if args.all {
					fmt.Printf("  %-30s -\n", p.Name)
				}
				continue
			}
			fmt.Printf("  %-30s looking...", p.Name)
			src, slug, ok := discover(hc, p)
			if !ok {
				missing++
				fmt.Printf("\r  %-30s not found\n", p.Name)
				continue
			}
			fmt.Printf("\r  %-30s %s   (via wago/%s)\n", p.Name, src, slug)
			discovered = append(discovered, [2]string{p.Name, src.String()})
			time.Sleep(400 * time.Millisecond) // be gentle with a public page
		}

		if len(discovered) > 0 {
			target := cfg.Path
			if target == "" {
				fatalf("no config file to write to; run 'wowbak config init' first")
			}
			for _, d := range discovered {
				if err := setConfValue(target, "addon."+strings.ToLower(d[0]), d[1]); err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not save %s: %v\n", d[0], err)
				}
			}
			fmt.Printf("\n  saved %d source(s) to %s\n", len(discovered), target)
		}
		fmt.Printf("\n  %d with a source, %d without\n\n", found+len(discovered), missing)
	}
	return 0
}
